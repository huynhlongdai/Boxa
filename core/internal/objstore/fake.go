package objstore

import (
	"crypto/hmac"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fake is an in-process S3-compatible server (path-style) that verifies
// SigV4 header signatures and presigned URLs. Used by tests (no network).
type Fake struct {
	AccessKey, Secret, Region string

	mu      sync.Mutex
	Objects map[string][]byte // "bucket/key" → data
	Calls   []string          // "METHOD bucket/key"
}

func NewFake(access, secret, region string) *Fake {
	return &Fake{AccessKey: access, Secret: secret, Region: region, Objects: map[string][]byte{}}
}

func (f *Fake) Get(bucketKey string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.Objects[bucketKey]
	return b, ok
}

func (f *Fake) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Objects)
}

func (f *Fake) deny(w http.ResponseWriter, code, msg string) {
	w.WriteHeader(403)
	w.Write([]byte("<Error><Code>" + code + "</Code><Message>" + msg + "</Message></Error>"))
}

func (f *Fake) verify(r *http.Request, body []byte) (bool, string) {
	q := r.URL.Query()
	if sig := q.Get("X-Amz-Signature"); sig != "" {
		amzDate := q.Get("X-Amz-Date")
		t, err := time.Parse("20060102T150405Z", amzDate)
		if err != nil {
			return false, "bad date"
		}
		var exp time.Duration
		if d, err := time.ParseDuration(q.Get("X-Amz-Expires") + "s"); err == nil {
			exp = d
		}
		if time.Now().UTC().After(t.Add(exp)) {
			return false, "expired"
		}
		cred := strings.SplitN(q.Get("X-Amz-Credential"), "/", 2)
		if len(cred) != 2 || cred[0] != f.AccessKey {
			return false, "bad access key"
		}
		q2 := url.Values{}
		for k, v := range q {
			if k != "X-Amz-Signature" {
				q2[k] = v
			}
		}
		canon := CanonicalRequest(r.Method, r.URL.EscapedPath(), canonicalQuery(q2), map[string]string{"host": r.Host}, []string{"host"}, "UNSIGNED-PAYLOAD")
		want := hex.EncodeToString(hmacSHA(SigningKey(f.Secret, amzDate[:8], f.Region, "s3"), StringToSign(amzDate, cred[1], canon)))
		return hmac.Equal([]byte(want), []byte(sig)), "signature mismatch"
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		return false, "missing auth"
	}
	parts := map[string]string{}
	for _, p := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ",") {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) == 2 {
			parts[kv[0]] = kv[1]
		}
	}
	cred := strings.SplitN(parts["Credential"], "/", 2)
	if len(cred) != 2 || cred[0] != f.AccessKey {
		return false, "bad access key"
	}
	if sha256Hex(body) != r.Header.Get("x-amz-content-sha256") {
		return false, "payload hash mismatch"
	}
	signed := strings.Split(parts["SignedHeaders"], ";")
	headers := map[string]string{}
	for _, h := range signed {
		if h == "host" {
			headers[h] = r.Host
		} else {
			headers[h] = r.Header.Get(h)
		}
	}
	amzDate := r.Header.Get("x-amz-date")
	if len(amzDate) < 8 {
		return false, "bad date"
	}
	canon := CanonicalRequest(r.Method, r.URL.EscapedPath(), canonicalQuery(r.URL.Query()), headers, signed, headers["x-amz-content-sha256"])
	want := hex.EncodeToString(hmacSHA(SigningKey(f.Secret, amzDate[:8], f.Region, "s3"), StringToSign(amzDate, cred[1], canon)))
	return hmac.Equal([]byte(want), []byte(parts["Signature"])), "signature mismatch"
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if ok, why := f.verify(r, body); !ok {
		f.deny(w, "SignatureDoesNotMatch", why)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(p, "/")
	bk := bucket + "/" + key
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, r.Method+" "+bk)
	switch r.Method {
	case "PUT":
		f.Objects[bk] = body
		w.WriteHeader(200)
	case "GET":
		if key == "" && r.URL.Query().Get("list-type") == "2" {
			type obj struct {
				Key          string `xml:"Key"`
				Size         int    `xml:"Size"`
				LastModified string `xml:"LastModified"`
			}
			var res struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []obj    `xml:"Contents"`
			}
			var keys []string
			for k := range f.Objects {
				if strings.HasPrefix(k, bucket+"/"+r.URL.Query().Get("prefix")) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				res.Contents = append(res.Contents, obj{Key: strings.TrimPrefix(k, bucket+"/"), Size: len(f.Objects[k]), LastModified: time.Now().UTC().Format(time.RFC3339)})
			}
			b, _ := xml.Marshal(res)
			w.Write(b)
			return
		}
		b, ok := f.Objects[bk]
		if !ok {
			w.WriteHeader(404)
			w.Write([]byte("<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>"))
			return
		}
		w.Write(b)
	case "HEAD":
		b, ok := f.Objects[bk]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", itoa(len(b)))
		w.WriteHeader(200)
	case "DELETE":
		delete(f.Objects, bk)
		w.WriteHeader(204)
	default:
		w.WriteHeader(405)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
