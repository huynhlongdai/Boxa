package sandbox

// M29-S: per-account templates and the error mapping sandbox accounts rely
// on (recorded-style: httptest servers answer like E2B / Daytona).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provhealth"
)

func TestE2BListTemplatesPerKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/templates" {
			http.NotFound(w, r)
			return
		}
		switch r.Header.Get("X-API-Key") {
		case "key-a":
			w.Write([]byte(`[{"templateID":"rki5dems9wqfm4r03t7g","aliases":["base"],"names":["e2b/base"]}]`))
		case "key-b":
			w.Write([]byte(`[{"templateID":"abc123","aliases":["agent"],"names":["proj-b/agent"]},{"templateID":"rki5dems9wqfm4r03t7g","aliases":["base"]}]`))
		default:
			w.WriteHeader(401)
			w.Write([]byte(`{"code":401,"message":"Invalid API key"}`))
		}
	}))
	defer srv.Close()
	d := NewE2B(srv.Client(), srv.URL)
	a, err := d.ListTemplates(context.Background(), Cred{"token": "key-a"})
	if err != nil || strings.Join(a, ",") != "rki5dems9wqfm4r03t7g,base,e2b/base" {
		t.Fatalf("key a: %v %v", a, err)
	}
	b, err := d.ListTemplates(context.Background(), Cred{"token": "key-b"})
	if err != nil || !contains(b, "agent") || !contains(b, "proj-b/agent") || !contains(b, "abc123") {
		t.Fatalf("key b: %v %v", b, err)
	}
	if _, err := d.ListTemplates(context.Background(), Cred{"token": "nope"}); provhealth.Classify(err) != provhealth.ClassAuth {
		t.Fatalf("bad key: %v", err)
	}
}

func TestDaytonaListSnapshots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/snapshots" || r.Header.Get("Authorization") != "Bearer dtn_x" {
			w.WriteHeader(403)
			return
		}
		w.Write([]byte(`{"items":[{"id":"s1","name":"py-agent","state":"active"},{"id":"s2","name":"building-one","state":"building"}]}`))
	}))
	defer srv.Close()
	d := NewDaytona(srv.Client(), srv.URL)
	l, err := d.ListTemplates(context.Background(), Cred{"token": "dtn_x"})
	if err != nil || strings.Join(l, ",") != "py-agent,s1" {
		t.Fatalf("snapshots %v %v", l, err)
	}
}

func TestSandboxErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conc":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
			w.Write([]byte(`{"code":429,"message":"You have reached the maximum number of concurrent E2B sandboxes (20)"}`))
		case "/daytona-limit":
			w.WriteHeader(429)
			w.Write([]byte(`{"code":"LIMIT_EXCEEDED","message":"organization limit reached","limit":"max_concurrent_vms","current":100,"max":100}`))
		case "/rate":
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(429)
			w.Write([]byte(`{"code":"RATE_LIMITED","message":"request rate limit exceeded"}`))
		case "/tpl":
			w.WriteHeader(404)
			w.Write([]byte(`{"code":404,"message":"template 'my-agent' not found"}`))
		case "/gone":
			w.WriteHeader(404)
			w.Write([]byte(`{"code":404,"message":"sandbox xyz not found"}`))
		}
	}))
	defer srv.Close()
	h := newHTTP(srv.Client())
	get := func(p string) error {
		return wrapErr("E2B", h.do(context.Background(), "GET", srv.URL+p, nil, nil, nil))
	}

	err := get("/conc")
	if provhealth.Classify(err) != provhealth.ClassQuota || retry(err) != 7 {
		t.Fatalf("concurrency 429: %v (%s, retry %d)", err, provhealth.Classify(err), retry(err))
	}
	if err := get("/daytona-limit"); provhealth.Classify(err) != provhealth.ClassQuota {
		t.Fatalf("daytona LIMIT_EXCEEDED: %v", err)
	}
	err = get("/rate")
	if provhealth.Classify(err) != provhealth.ClassRateLimit || retry(err) != 42 {
		t.Fatalf("rate 429: %v retry %d", err, retry(err))
	}
	if err := get("/tpl"); !IsTemplateMissing(err) {
		t.Fatalf("template 404 not recognised: %v", err)
	}
	if err := get("/gone"); IsTemplateMissing(err) {
		t.Fatalf("a missing sandbox is not a missing template: %v", err)
	}
	if !GlobalTemplate(Info{Sizes: []Size{{SKU: "base"}}}, "base") || !GlobalTemplate(Info{}, "team/x") || GlobalTemplate(Info{}, "mine") {
		t.Fatal("GlobalTemplate")
	}
}

func retry(err error) int {
	if ae, ok := err.(*model.APIError); ok && ae.RetryAfterS != nil {
		return *ae.RetryAfterS
	}
	return 0
}

func contains(l []string, x string) bool {
	for _, y := range l {
		if y == x {
			return true
		}
	}
	return false
}
