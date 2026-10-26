package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDigitalOceanAdapter(t *testing.T) {
	var created map[string]any
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(401)
			w.Write([]byte(`{"id":"unauthorized"}`))
			return
		}
		switch {
		case r.URL.Path == "/account":
			w.Write([]byte(`{"account":{"status":"active"}}`))
		case r.URL.Path == "/sizes":
			w.Write([]byte(`{"sizes":[{"slug":"s-1vcpu-1gb","vcpus":1,"memory":1024,"disk":25,"price_hourly":0.00893,"price_monthly":6,"regions":["sgp1","fra1"],"available":true},{"slug":"old","available":false}],"links":{"pages":{}}}`))
		case r.URL.Path == "/regions":
			w.Write([]byte(`{"regions":[{"slug":"sgp1","name":"Singapore 1","available":true}]}`))
		case r.URL.Path == "/droplets" && r.Method == "POST":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &created)
			w.WriteHeader(202)
			w.Write([]byte(`{"droplet":{"id":77,"status":"new","networks":{"v4":[{"ip_address":"10.0.0.2","type":"private"},{"ip_address":"5.6.7.8","type":"public"}]}}}`))
		case r.URL.Path == "/droplets" && r.Method == "GET":
			if r.URL.Query().Get("tag_name") != "boxa" {
				t.Errorf("list must filter tag boxa")
			}
			w.Write([]byte(`{"droplets":[{"id":77,"name":"boxa-1","status":"active","size_slug":"s-1vcpu-1gb","region":{"slug":"sgp1"},"networks":{"v4":[{"ip_address":"5.6.7.8","type":"public"}]}}]}`))
		case r.URL.Path == "/droplets/77/actions":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			actions = append(actions, b["type"].(string))
			w.WriteHeader(201)
			w.Write([]byte(`{"action":{}}`))
		case r.URL.Path == "/droplets/77" && r.Method == "DELETE":
			w.WriteHeader(204)
		case r.URL.Path == "/droplets/77":
			w.Write([]byte(`{"droplet":{"id":77,"status":"active"}}`))
		case r.URL.Path == "/droplets/404":
			w.WriteHeader(404)
			w.Write([]byte(`{"id":"not_found"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	d := NewDigitalOcean(srv.Client())
	d.BaseURL = srv.URL
	ctx := context.Background()
	if err := d.Validate(ctx, "bad"); err == nil || !strings.Contains(err.Error(), "UNAUTHORIZED") {
		t.Fatalf("bad token: %v", err)
	}
	if err := d.Validate(ctx, "good"); err != nil {
		t.Fatal(err)
	}
	offers, err := d.Offers(ctx, "good")
	if err != nil || len(offers) != 2 || offers[0].PriceHour != 0.00893 || offers[0].Confidence != "verified" || offers[1].Region != "fra1" {
		t.Fatalf("offers %+v %v", offers, err)
	}
	rs, _ := d.Regions(ctx, "good")
	if len(rs) != 1 || rs[0].ID != "sgp1" {
		t.Fatalf("regions %+v", rs)
	}
	hd, err := d.Create(ctx, "good", CreateRequest{Name: "boxa-1", SKU: "s-1vcpu-1gb", Region: "sgp1", CloudInit: "#cloud-config\n", Labels: map[string]string{"boxa-host": "abc"}})
	if err != nil || hd.Ref != "77" || hd.IP != "5.6.7.8" {
		t.Fatalf("create %+v %v", hd, err)
	}
	if created["image"] != "ubuntu-24-04-x64" || created["user_data"] != "#cloud-config\n" || created["size"] != "s-1vcpu-1gb" {
		t.Fatalf("body %+v", created)
	}
	ls, err := d.List(ctx, "good")
	if err != nil || len(ls) != 1 || ls[0].Status != "ready" || ls[0].IP != "5.6.7.8" {
		t.Fatalf("list %+v %v", ls, err)
	}
	if st, _ := d.Status(ctx, "good", "77"); st != "ready" {
		t.Fatalf("status %s", st)
	}
	if st, _ := d.Status(ctx, "good", "404"); st != "down" {
		t.Fatalf("404 status %s", st)
	}
	d.Sleep(ctx, "good", "77")
	d.Wake(ctx, "good", "77")
	if strings.Join(actions, ",") != "power_off,power_on" {
		t.Fatalf("actions %v", actions)
	}
	if err := d.Destroy(ctx, "good", "77"); err != nil {
		t.Fatal(err)
	}
}

func TestContaboAdapter(t *testing.T) {
	var mu sync.Mutex
	tokenCalls := 0
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth" {
			r.ParseForm()
			mu.Lock()
			tokenCalls++
			mu.Unlock()
			if r.Form.Get("grant_type") != "password" || r.Form.Get("client_id") != "cid" || r.Form.Get("password") != "pw" {
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Write([]byte(`{"access_token":"AT","expires_in":300}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer AT" || r.Header.Get("x-request-id") == "" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/compute/instances" && r.Method == "GET":
			w.Write([]byte(`{"data":[{"instanceId":101,"displayName":"boxa-x","status":"running","productId":"V91","region":"EU","ipConfig":{"v4":{"ip":"9.9.9.9"}}}]}`))
		case r.URL.Path == "/compute/images":
			w.Write([]byte(`{"data":[{"imageId":"img-u24","name":"ubuntu-24.04"}]}`))
		case r.URL.Path == "/compute/instances" && r.Method == "POST":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &created)
			w.WriteHeader(201)
			w.Write([]byte(`{"data":[{"instanceId":101,"status":"provisioning"}]}`))
		case r.URL.Path == "/compute/instances/101" && r.Method == "GET":
			w.Write([]byte(`{"data":[{"instanceId":101,"status":"installing"}]}`))
		case r.URL.Path == "/compute/instances/101/cancel", r.URL.Path == "/compute/instances/101/actions/stop", r.URL.Path == "/compute/instances/101/actions/start":
			w.Write([]byte(`{"data":[{}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	k := NewContabo(srv.Client())
	k.BaseURL, k.AuthURL = srv.URL, srv.URL+"/auth"
	ctx := context.Background()
	tok := TokenFromEnv("contabo", func(key string) string {
		return map[string]string{"CONTABO_CLIENT_ID": "cid", "CONTABO_CLIENT_SECRET": "cs", "CONTABO_API_USER": "u@x", "CONTABO_API_PASSWORD": "pw"}[key]
	})
	if tok == "" {
		t.Fatal("token from env")
	}
	if err := k.Validate(ctx, "cid:cs:u@x:wrong"); err == nil || !strings.Contains(err.Error(), "UNAUTHORIZED") {
		t.Fatalf("bad creds: %v", err)
	}
	if err := k.Validate(ctx, "garbage"); err == nil || !strings.Contains(err.Error(), "INVALID") {
		t.Fatalf("garbage creds: %v", err)
	}
	if err := k.Validate(ctx, tok); err != nil {
		t.Fatal(err)
	}
	offers, _ := k.Offers(ctx, tok)
	if len(offers) == 0 || offers[0].Confidence != "unverified" || offers[0].MinBillingMinutes != 730*60 {
		t.Fatalf("offers %+v", offers[:1])
	}
	hd, err := k.Create(ctx, tok, CreateRequest{Name: "boxa-1", SKU: "V91", Region: "EU", CloudInit: "#cloud-config\n"})
	if err != nil || hd.Ref != "101" {
		t.Fatalf("create %+v %v", hd, err)
	}
	if created["imageId"] != "img-u24" || created["productId"] != "V91" || created["userData"] != "#cloud-config\n" {
		t.Fatalf("body %+v", created)
	}
	if st, _ := k.Status(ctx, tok, "101"); st != "provisioning" {
		t.Fatalf("status %s", st)
	}
	ls, err := k.List(ctx, tok)
	if err != nil || len(ls) != 1 || ls[0].Status != "ready" {
		t.Fatalf("list %+v %v", ls, err)
	}
	if err := k.Sleep(ctx, tok, "101"); err != nil {
		t.Fatal(err)
	}
	if err := k.Destroy(ctx, tok, "101"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if tokenCalls != 2 { // wrong creds + one cached exchange for the good token (garbage never reaches auth)
		t.Fatalf("token exchanges %d", tokenCalls)
	}
}

func TestNetcupAdapter(t *testing.T) {
	var patches []map[string]any
	var image map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			r.ParseForm()
			if r.Form.Get("refresh_token") != "RT" || r.Form.Get("client_id") != "scp" {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Write([]byte(`{"access_token":"NAT","expires_in":300}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer NAT" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/servers" && r.Method == "GET":
			w.Write([]byte(`[{"id":1,"nickname":"prod-db","template":{"name":"RS 1000 G12"}},{"id":2,"nickname":"boxa-spare-1","template":{"name":"VPS 1000 G11"},"site":{"city":"nue"},"serverLiveInfo":{"state":"SHUTOFF"},"ipv4Addresses":[{"ip":"3.3.3.3"}]}]`))
		case r.URL.Path == "/servers/2/imageflavours":
			w.Write([]byte(`[{"id":11,"name":"Debian 12"},{"id":12,"name":"Ubuntu 24.04 LTS"}]`))
		case r.URL.Path == "/servers/2" && r.Method == "PATCH":
			if r.Header.Get("Content-Type") != "application/merge-patch+json" {
				t.Errorf("patch content type %q", r.Header.Get("Content-Type"))
			}
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			patches = append(patches, b)
			w.Write([]byte(`{}`))
		case r.URL.Path == "/servers/2/image":
			json.NewDecoder(r.Body).Decode(&image)
			w.WriteHeader(202)
			w.Write([]byte(`{"uuid":"task"}`))
		case r.URL.Path == "/servers/2":
			w.Write([]byte(`{"id":2,"serverLiveInfo":{"state":"RUNNING"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	n := NewNetcup(srv.Client())
	n.BaseURL, n.AuthURL = srv.URL, srv.URL+"/token"
	ctx := context.Background()
	if err := n.Validate(ctx, "nope"); err == nil {
		t.Fatal("bad refresh token should fail")
	}
	if err := n.Validate(ctx, "RT"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Create(ctx, "RT", CreateRequest{Name: "boxa-9", SKU: "RS 2000 G12", CloudInit: "x"}); err == nil || !strings.Contains(err.Error(), "NO_CAPACITY") {
		t.Fatalf("no spare for sku: %v", err)
	}
	hd, err := n.Create(ctx, "RT", CreateRequest{Name: "boxa-9", SKU: "VPS 1000 G11", CloudInit: "#cloud-config\n"})
	if err != nil || hd.Ref != "2" || hd.IP != "3.3.3.3" {
		t.Fatalf("create %+v %v", hd, err)
	}
	if image["imageFlavourId"].(float64) != 12 || image["customScript"] != "#cloud-config\n" || patches[0]["nickname"] != "boxa-9" {
		t.Fatalf("image %+v patches %+v", image, patches)
	}
	if st, _ := n.Status(ctx, "RT", "2"); st != "ready" {
		t.Fatalf("status %s", st)
	}
	if err := n.Destroy(ctx, "RT", "2"); err != nil {
		t.Fatal(err)
	}
	last := patches[len(patches)-1]
	if last["nickname"] != "boxa-spare-2" || patches[len(patches)-2]["state"] != "OFF" {
		t.Fatalf("destroy patches %+v", patches)
	}
	ls, _ := n.List(ctx, "RT")
	if len(ls) != 2 || ls[1].Name != "boxa-spare-1" {
		t.Fatalf("list %+v", ls)
	}
}

func TestTokenFromEnv(t *testing.T) {
	env := map[string]string{"DIGITALOCEAN_TOKEN": " dop_v1_x ", "CONTABO_CLIENT_ID": "a"}
	g := func(k string) string { return env[k] }
	if TokenFromEnv("digitalocean", g) != "dop_v1_x" {
		t.Fatal("do token")
	}
	if TokenFromEnv("contabo", g) != "" {
		t.Fatal("partial contabo creds must be ignored")
	}
	if TokenFromEnv("local", g) != "" {
		t.Fatal("local has no token")
	}
}
