package objstore

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSigV4KnownVector(t *testing.T) {
	// AWS documentation example: signing key for wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY, 20120215, us-east-1, iam.
	k := SigningKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20120215", "us-east-1", "iam")
	want := "f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d"
	if got := hexs(k); got != want {
		t.Fatalf("signing key %s", got)
	}
}

func hexs(b []byte) string {
	const h = "0123456789abcdef"
	var s strings.Builder
	for _, c := range b {
		s.WriteByte(h[c>>4])
		s.WriteByte(h[c&15])
	}
	return s.String()
}

func TestClientAgainstFake(t *testing.T) {
	fake := NewFake("AK", "SK", "auto")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := New(Config{Provider: "r2", Endpoint: srv.URL, Bucket: "snaps", Prefix: "/boxaz/", AccessKeyID: "AK", SecretAccessKey: "SK"}, srv.Client())
	if c.Cfg.Region != "auto" || c.Key("a b.tgz") != "boxaz/a b.tgz" {
		t.Fatalf("defaults %+v", c.Cfg)
	}
	ctx := context.Background()
	if err := c.Test(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, c.Key("snap 1.tar.gz"), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if b, ok := fake.Get("snaps/boxaz/snap 1.tar.gz"); !ok || string(b) != "hello" {
		t.Fatalf("stored %q %v", b, ok)
	}
	if n, err := c.Head(ctx, c.Key("snap 1.tar.gz")); err != nil || n != 5 {
		t.Fatalf("head %d %v", n, err)
	}
	objs, err := c.List(ctx, "boxaz/")
	if err != nil || len(objs) != 1 || objs[0].Size != 5 {
		t.Fatalf("list %+v %v", objs, err)
	}
	// presigned PUT + GET without credentials (what boxd does)
	put, _ := c.Presign("PUT", c.Key("p.tgz"), 10*time.Minute)
	req, _ := http.NewRequest("PUT", put, bytes.NewReader([]byte("tarball")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("presigned put %v %v", resp, err)
	}
	get, _ := c.Presign("GET", c.Key("p.tgz"), time.Minute)
	resp, err = http.Get(get)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("presigned get %v %v", resp, err)
	}
	// tampered signature / wrong secret are rejected
	resp, _ = http.Get(strings.Replace(get, "p.tgz", "q.tgz", 1))
	if resp.StatusCode != 403 {
		t.Fatalf("tampered url status %d", resp.StatusCode)
	}
	bad := New(Config{Endpoint: srv.URL, Bucket: "snaps", AccessKeyID: "AK", SecretAccessKey: "nope"}, srv.Client())
	if err := bad.Put(ctx, "x", []byte("1")); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("wrong secret: %v", err)
	}
	// expired presign
	old := New(c.Cfg, srv.Client())
	old.Now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	exp, _ := old.Presign("GET", c.Key("p.tgz"), time.Minute)
	resp, _ = http.Get(exp)
	if resp.StatusCode != 403 {
		t.Fatalf("expired url status %d", resp.StatusCode)
	}
	if err := c.Delete(ctx, c.Key("p.tgz")); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.Get("snaps/boxaz/p.tgz"); ok {
		t.Fatal("not deleted")
	}
	if k, ok := c.KeyFromURI(c.URI("boxaz/x")); !ok || k != "boxaz/x" {
		t.Fatal("uri roundtrip")
	}
	b2 := Config{Provider: "b2", Endpoint: "s3.us-west-004.backblazeb2.com"}
	b2.Defaults()
	if b2.Region != "us-west-004" || b2.Endpoint != "https://s3.us-west-004.backblazeb2.com" {
		t.Fatalf("b2 defaults %+v", b2)
	}
}
