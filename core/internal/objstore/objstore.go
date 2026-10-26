// Package objstore is a minimal S3-compatible client (AWS Signature V4) for
// snapshot storage on Cloudflare R2, Backblaze B2 (S3 API), AWS S3, MinIO…
// It needs no SDK: Put/Get/Delete/Head/List from core, and presigned URLs so
// boxd can stream snapshot tarballs straight to/from the bucket without the
// keys ever leaving core.
package objstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config describes a bucket. Provider is informational (r2|b2|s3|minio).
type Config struct {
	Provider        string `json:"provider"`
	Endpoint        string `json:"endpoint"` // https://<account>.r2.cloudflarestorage.com, https://s3.<region>.backblazeb2.com
	Region          string `json:"region"`   // "auto" for R2, e.g. "us-west-004" for B2
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"-"`
	PathStyle       bool   `json:"path_style"` // default true (R2/B2/MinIO all accept path-style)
}

// Enabled reports whether the config is complete.
func (c *Config) Enabled() bool {
	return c != nil && c.Endpoint != "" && c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

// Defaults fills region by provider.
func (c *Config) Defaults() {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if c.Endpoint != "" && !strings.Contains(c.Endpoint, "://") {
		c.Endpoint = "https://" + c.Endpoint
	}
	if c.Region == "" {
		switch c.Provider {
		case "r2":
			c.Region = "auto"
		case "b2":
			// s3.<region>.backblazeb2.com
			if u, err := url.Parse(c.Endpoint); err == nil {
				parts := strings.Split(u.Hostname(), ".")
				if len(parts) >= 3 && parts[0] == "s3" {
					c.Region = parts[1]
				}
			}
		}
		if c.Region == "" {
			c.Region = "us-east-1"
		}
	}
	c.Prefix = strings.Trim(c.Prefix, "/")
}

// Client signs requests for one bucket.
type Client struct {
	Cfg  Config
	HTTP *http.Client
	Now  func() time.Time
}

func New(cfg Config, hc *http.Client) *Client {
	cfg.Defaults()
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Client{Cfg: cfg, HTTP: hc, Now: time.Now}
}

// Key joins the configured prefix with name.
func (c *Client) Key(name string) string {
	if c.Cfg.Prefix == "" {
		return name
	}
	return c.Cfg.Prefix + "/" + name
}

// URI is the canonical s3:// location of a key.
func (c *Client) URI(key string) string { return "s3://" + c.Cfg.Bucket + "/" + key }

// KeyFromURI extracts the key from s3://bucket/key (ok=false for other schemes/buckets).
func (c *Client) KeyFromURI(uri string) (string, bool) {
	p := "s3://" + c.Cfg.Bucket + "/"
	if !strings.HasPrefix(uri, p) {
		return "", false
	}
	return strings.TrimPrefix(uri, p), true
}

func (c *Client) objectURL(key string, q url.Values) (*url.URL, error) {
	u, err := url.Parse(c.Cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	// Path-style addressing (R2, B2 and MinIO all accept it).
	u.Path = "/" + c.Cfg.Bucket + "/" + key
	u.RawPath = "/" + uriEncode(c.Cfg.Bucket) + "/" + escapePath(key)
	if q != nil {
		u.RawQuery = canonicalQuery(q)
	}
	return u, nil
}

// escapePath URI-encodes each path segment per SigV4 (unreserved kept, '/' kept).
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s)
	}
	return strings.Join(segs, "/")
}

func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k)+"="+uriEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

func hmacSHA(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// SigningKey derives the SigV4 key.
func SigningKey(secret, date, region, service string) []byte {
	k := hmacSHA([]byte("AWS4"+secret), date)
	k = hmacSHA(k, region)
	k = hmacSHA(k, service)
	return hmacSHA(k, "aws4_request")
}

// CanonicalRequest builds the SigV4 canonical request string. headers must
// include host; signed lists the lowercase header names in order.
func CanonicalRequest(method, path, rawQuery string, headers map[string]string, signed []string, payloadHash string) string {
	var ch strings.Builder
	for _, h := range signed {
		ch.WriteString(h + ":" + strings.TrimSpace(headers[h]) + "\n")
	}
	return strings.Join([]string{method, path, rawQuery, ch.String(), strings.Join(signed, ";"), payloadHash}, "\n")
}

// StringToSign builds the SigV4 string to sign.
func StringToSign(amzDate, scope, canonical string) string {
	return "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
}

// Presign returns a URL valid for ttl that performs method on key (GET/PUT/DELETE/HEAD).
func (c *Client) Presign(method, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > 7*24*time.Hour {
		ttl = time.Hour
	}
	now := c.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	scope := date + "/" + c.Cfg.Region + "/s3/aws4_request"
	q := url.Values{
		"X-Amz-Algorithm":     {"AWS4-HMAC-SHA256"},
		"X-Amz-Credential":    {c.Cfg.AccessKeyID + "/" + scope},
		"X-Amz-Date":          {amzDate},
		"X-Amz-Expires":       {strconv.Itoa(int(ttl.Seconds()))},
		"X-Amz-SignedHeaders": {"host"},
	}
	u, err := c.objectURL(key, q)
	if err != nil {
		return "", err
	}
	canon := CanonicalRequest(method, u.RawPath, u.RawQuery, map[string]string{"host": u.Host}, []string{"host"}, "UNSIGNED-PAYLOAD")
	sig := hex.EncodeToString(hmacSHA(SigningKey(c.Cfg.SecretAccessKey, date, c.Cfg.Region, "s3"), StringToSign(amzDate, scope, canon)))
	u.RawQuery += "&X-Amz-Signature=" + sig
	return u.String(), nil
}

// do sends a header-signed request.
func (c *Client) do(ctx context.Context, method, key string, q url.Values, body []byte, extra map[string]string) (*http.Response, error) {
	u, err := c.objectURL(key, q)
	if err != nil {
		return nil, err
	}

	now := c.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	scope := date + "/" + c.Cfg.Region + "/s3/aws4_request"
	ph := sha256Hex(body)
	headers := map[string]string{"host": u.Host, "x-amz-content-sha256": ph, "x-amz-date": amzDate}
	for k, v := range extra {
		headers[strings.ToLower(k)] = v
	}
	signed := make([]string, 0, len(headers))
	for k := range headers {
		signed = append(signed, k)
	}
	sort.Strings(signed)
	canon := CanonicalRequest(method, u.RawPath, u.RawQuery, headers, signed, ph)
	sig := hex.EncodeToString(hmacSHA(SigningKey(c.Cfg.SecretAccessKey, date, c.Cfg.Region, "s3"), StringToSign(amzDate, scope, canon)))
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		if k != "host" {
			req.Header.Set(k, v)
		}
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.Cfg.AccessKeyID, scope, strings.Join(signed, ";"), sig))
	return c.HTTP.Do(req)
}

// Error is a non-2xx S3 response.
type Error struct {
	Status int
	Code   string
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("s3 %d %s: %s", e.Status, e.Code, e.Msg) }

func respErr(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var x struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	xml.Unmarshal(b, &x)
	if x.Message == "" {
		x.Message = strings.TrimSpace(string(b))
	}
	return &Error{Status: resp.StatusCode, Code: x.Code, Msg: x.Message}
}

// Put uploads a small object from memory.
func (c *Client) Put(ctx context.Context, key string, data []byte) error {
	resp, err := c.do(ctx, "PUT", key, nil, data, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return respErr(resp)
	}
	return nil
}

// Get downloads an object into memory.
func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.do(ctx, "GET", key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, respErr(resp)
	}
	return io.ReadAll(resp.Body)
}

// Head returns the object size (Error.Status 404 when missing).
func (c *Client) Head(ctx context.Context, key string) (int64, error) {
	resp, err := c.do(ctx, "HEAD", key, nil, nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, &Error{Status: resp.StatusCode, Code: http.StatusText(resp.StatusCode)}
	}
	return resp.ContentLength, nil
}

// Delete removes an object (missing objects are not an error).
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, "DELETE", key, nil, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 404 {
		return respErr(resp)
	}
	return nil
}

// Object is one ListObjectsV2 entry.
type Object struct {
	Key          string    `xml:"Key" json:"key"`
	Size         int64     `xml:"Size" json:"size"`
	LastModified time.Time `xml:"LastModified" json:"last_modified"`
}

// List returns objects under prefix (first 1000).
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {"1000"}}
	resp, err := c.do(ctx, "GET", "", q, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, respErr(resp)
	}
	var out struct {
		Contents []Object `xml:"Contents"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Contents, nil
}

// Test writes, reads back and deletes a probe object.
func (c *Client) Test(ctx context.Context) error {
	key := c.Key(".boxa-probe-" + strconv.FormatInt(c.Now().UnixNano(), 36))
	data := []byte("boxa storage probe")
	if err := c.Put(ctx, key, data); err != nil {
		return fmt.Errorf("put: %w", err)
	}
	got, err := c.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	if !bytes.Equal(got, data) {
		return fmt.Errorf("get: content mismatch")
	}
	if err := c.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}
