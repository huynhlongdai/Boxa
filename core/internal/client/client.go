// Package client is a minimal REST client for the Boxa API (used by the CLI
// and the stdio MCP server).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string // e.g. http://localhost:8080 (without /v1)
	Key     string
	Lang    string // Accept-Language sent with every call (vi|en|zh|ja); "" = server default (en)
	HTTP    *http.Client
}

func New(base, key string) *Client {
	return &Client{BaseURL: strings.TrimRight(strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1"), "/"), Key: key, HTTP: &http.Client{Timeout: 70 * time.Minute}}
}

// Do implements mcp.Caller: path is relative to /v1.
func (c *Client) Do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/v1"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if c.Lang != "" {
		req.Header.Set("Accept-Language", c.Lang)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// APIError mirrors the error envelope.
type APIError struct {
	Status      int
	Code        string `json:"code"`
	Message     string `json:"message"`
	Hint        string `json:"hint"`
	RetryAfterS *int   `json:"retry_after_s,omitempty"`
	DocsURL     string `json:"docs_url,omitempty"`
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("%s: %s", e.Code, e.Message)
	if e.Hint != "" {
		s += " (hint: " + e.Hint + ")"
	}
	return s
}

// JSON performs a call and decodes into out, converting error envelopes.
func (c *Client) JSON(ctx context.Context, method, path string, body, out any) error {
	st, b, err := c.Do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if st >= 300 {
		var env struct {
			Error APIError `json:"error"`
		}
		if json.Unmarshal(b, &env) == nil && env.Error.Code != "" {
			env.Error.Status = st
			return &env.Error
		}
		return &APIError{Status: st, Code: fmt.Sprint(st), Message: strings.TrimSpace(string(b))}
	}
	if out != nil {
		if raw, ok := out.(*json.RawMessage); ok {
			*raw = append((*raw)[:0], b...)
			return nil
		}
		return json.Unmarshal(b, out)
	}
	return nil
}
