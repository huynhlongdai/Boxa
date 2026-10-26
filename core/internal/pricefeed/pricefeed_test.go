package pricefeed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestConvertHetznerFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/hetzner-cloud.json")
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseProvider("hetzner-cloud", raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Provider != "hetzner" || len(r.Offers) == 0 {
		t.Fatalf("got %+v", r)
	}
	if r.Capabilities["snapshot_any"] != true {
		t.Fatalf("capabilities missing: %v", r.Capabilities)
	}
	var stock, unv int
	for _, o := range r.Offers {
		if o.PriceHour <= 0 || o.CPU <= 0 || o.RAMMB <= 0 || o.Source != "battleships" || o.MinBillingMinutes != 60 {
			t.Fatalf("bad offer %+v", o)
		}
		switch o.Confidence {
		case "stock_limited":
			stock++
		case "unverified":
			unv++
		}
	}
	if stock == 0 || unv == 0 {
		t.Fatalf("confidence mapping: stock=%d unverified=%d", stock, unv)
	}
}

func TestFetchToleratesFailures(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/hetzner-cloud.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data/index.json":
			w.Write([]byte(`{"generated":"2026-10-14","count":2,"providers":[{"id":"hetzner-cloud","json":"x"},{"id":"vultr","json":"y"}]}`))
		case "/data/providers/hetzner-cloud.json":
			w.Write(fixture)
		default:
			http.Error(w, "boom", 500)
		}
	}))
	defer srv.Close()
	f := &Fetcher{BaseURL: srv.URL, Allowlist: []string{"hetzner-cloud", "vultr", "contabo"}}
	rs, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 3 || rs[0].Err != nil || rs[1].Err == nil || rs[2].Err == nil {
		t.Fatalf("results: %+v", rs)
	}
	// Whole feed down → error but no panic.
	f2 := &Fetcher{BaseURL: "http://127.0.0.1:1", Allowlist: []string{"vultr"}, HTTP: &http.Client{Timeout: time.Second}}
	if _, err := f2.Fetch(context.Background()); err == nil {
		t.Fatal("expected error when feed unreachable")
	}
}
