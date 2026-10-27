package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boxaz/core/internal/model"
)

func TestQuote(t *testing.T) {
	o := model.Offer{PriceHour: 0.01, PriceMonth: 5, MinBillingMinutes: 60}
	if q := Quote(o, 0.1, 0); q != 0.01 {
		t.Fatalf("min billing: %v", q)
	}
	if q := Quote(o, 3, 0.002); q < 0.0319 || q > 0.0321 {
		t.Fatalf("3h: %v", q)
	}
	if q := Quote(o, 730, 0); q != 5 {
		t.Fatalf("monthly cap: %v", q)
	}
}

func TestHetznerAdapter(t *testing.T) {
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"code":"unauthorized"}}`))
			return
		}
		switch {
		case r.URL.Path == "/locations":
			w.Write([]byte(`{"locations":[{"name":"fsn1"}]}`))
		case r.URL.Path == "/server_types":
			w.Write([]byte(`{"server_types":[
			 {"name":"cx22","cores":2,"memory":4.0,"disk":40,"deprecation":null,"prices":[{"location":"fsn1","price_hourly":{"net":"0.0060","gross":"0.0071"},"price_monthly":{"net":"3.7900","gross":"4.51"}}]},
			 {"name":"cx11","cores":1,"memory":2.0,"disk":20,"deprecated":true,"prices":[]}],
			 "meta":{"pagination":{"next_page":null}}}`))
		case r.URL.Path == "/servers" && r.Method == "POST":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &created)
			w.Write([]byte(`{"server":{"id":42,"public_net":{"ipv4":{"ip":"1.2.3.4"}}}}`))
		case r.URL.Path == "/servers/42" && r.Method == "DELETE":
			w.Write([]byte(`{"action":{}}`))
		case r.URL.Path == "/servers/42":
			w.Write([]byte(`{"server":{"status":"running"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	h := NewHetzner(srv.Client())
	h.BaseURL = srv.URL
	ctx := context.Background()
	if err := h.Validate(ctx, "bad"); err == nil || !strings.Contains(err.Error(), "UNAUTHORIZED") {
		t.Fatalf("bad token: %v", err)
	}
	if err := h.Validate(ctx, "good"); err != nil {
		t.Fatal(err)
	}
	offers, err := h.Offers(ctx, "good")
	if err != nil || len(offers) != 1 || offers[0].SKU != "cx22" || offers[0].PriceHour != 0.006 || offers[0].RAMMB != 4096 || offers[0].Confidence != "verified" {
		t.Fatalf("offers: %+v %v", offers, err)
	}
	ci, _ := RenderCloudInit(CloudInitParams{CoreURL: "wss://core/v1/hosts/connect", BootstrapToken: "bxb_x", Provider: "hetzner", Region: "fsn1", BoxdURL: "https://core/dl/boxd"})
	hd, err := h.Create(ctx, "good", CreateRequest{Name: "mgr-1", SKU: "cx22", Region: "fsn1", CloudInit: ci})
	if err != nil || hd.Ref != "42" || hd.IP != "1.2.3.4" {
		t.Fatalf("create: %+v %v", hd, err)
	}
	if created["server_type"] != "cx22" || !strings.Contains(created["user_data"].(string), "BOXD_TOKEN=bxb_x") {
		t.Fatalf("create body: %+v", created)
	}
	if st, _ := h.Status(ctx, "good", "42"); st != "ready" {
		t.Fatalf("status %s", st)
	}
	if err := h.Destroy(ctx, "good", "42"); err != nil {
		t.Fatal(err)
	}
}

func TestVultrAdapter(t *testing.T) {
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/account":
			w.Write([]byte(`{"account":{"email":"a@b","acls":["manage_servers","billing"]}}`))
		case r.URL.Path == "/plans":
			w.Write([]byte(`{"plans":[{"id":"vc2-1c-1gb","vcpu_count":1,"ram":1024,"disk":25,"monthly_cost":5,"hourly_cost":0.007,"type":"vc2","locations":["ewr","sgp"]}],"meta":{"links":{"next":""}}}`))
		case r.URL.Path == "/os":
			w.Write([]byte(`{"os":[{"id":2284,"name":"Ubuntu 24.04 LTS x64","arch":"x64","family":"ubuntu"}]}`))
		case r.URL.Path == "/instances" && r.Method == "POST":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &created)
			w.Write([]byte(`{"instance":{"id":"abc","main_ip":"0.0.0.0"}}`))
		case r.URL.Path == "/instances/abc" && r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	v := NewVultr(srv.Client())
	v.BaseURL = srv.URL
	ctx := context.Background()
	if err := v.Validate(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	offers, err := v.Offers(ctx, "t")
	if err != nil || len(offers) != 2 || offers[1].Region != "sgp" {
		t.Fatalf("offers %+v %v", offers, err)
	}
	hd, err := v.Create(ctx, "t", CreateRequest{Name: "n", SKU: "vc2-1c-1gb", Region: "sgp", CloudInit: "#cloud-config\n"})
	if err != nil || hd.Ref != "abc" {
		t.Fatalf("create %+v %v", hd, err)
	}
	ud, _ := base64.StdEncoding.DecodeString(created["user_data"].(string))
	if created["os_id"].(float64) != 2284 || string(ud) != "#cloud-config\n" {
		t.Fatalf("body %+v", created)
	}
	if err := v.Destroy(ctx, "t", "abc"); err != nil {
		t.Fatal(err)
	}
}

func TestStubsAreHonest(t *testing.T) {
	r := Default(nil)
	for _, id := range []string{"hetzner", "vultr", "digitalocean", "contabo", "netcup"} {
		p, ok := r.Get(id)
		if !ok || !p.Implemented() {
			t.Fatalf("%s should have a real adapter", id)
		}
	}
	s := &Stub{id: "x", name: "X"}
	if err := s.Validate(context.Background(), "x"); err == nil {
		t.Fatal("stub validate should fail")
	}
}
