package mesh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRenderConfigAndDERP(t *testing.T) {
	if _, err := RenderConfig(Config{}); err == nil {
		t.Fatal("server_url required")
	}
	y, err := RenderConfig(Config{ServerURL: "https://mesh.example.com", EmbeddedDERP: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server_url: https://mesh.example.com", "base_domain: boxa.mesh", "region_id: 999", "enabled: true", "path: /etc/headscale/acl.json"} {
		if !strings.Contains(y, want) {
			t.Fatalf("config missing %q:\n%s", want, y)
		}
	}
	if d := RenderDERP(Config{}); !strings.Contains(d, "regions: {}") {
		t.Fatal(d)
	}
	if d := RenderDERP(Config{DERPHost: "derp.example.com"}); !strings.Contains(d, "hostname: derp.example.com") || !strings.Contains(d, "regionid: 1000") {
		t.Fatal(d)
	}
}

func TestGeneratePolicy(t *testing.T) {
	p := GeneratePolicy(Config{}, []Box{
		{ID: "box_b", AgentID: "agt_1", Ports: []int{8080, 3000}, EgressAllow: []string{"10.1.0.0/16", "1.2.3.4:443", "api.openai.com"}},
		{ID: "box_a", AgentID: "agt_1"},
		{ID: "box_c", AgentID: "agt_2", Ports: []int{80}},
	})
	js := p.JSON()
	for _, want := range []string{`"tag:agent-agt-1"`, `"tag:box-box-b:8080,3000"`, `"10.1.0.0/16:*"`, `"1.2.3.4:443"`, `"tag:box-box-c:80"`, `"tag:boxa-core:443,8080"`} {
		if !strings.Contains(js, want) {
			t.Fatalf("policy missing %s:\n%s", want, js)
		}
	}
	// agent 1 may not reach agent 2's box
	for _, a := range p.ACLs {
		if a.Src[0] == "tag:agent-agt-1" {
			for _, d := range a.Dst {
				if strings.Contains(d, "box-c") {
					t.Fatal("cross-agent access")
				}
			}
		}
	}
	if len(p.Notes) != 1 || !strings.Contains(p.Notes[0], "api.openai.com") {
		t.Fatalf("notes %v", p.Notes)
	}
	if _, ok := aclDest("example.com:443"); ok {
		t.Fatal("domain must not become an ACL dest")
	}
}

func TestClient(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/user":
			if r.Method == "GET" {
				w.Write([]byte(`{"users":[{"name":"boxa"}]}`))
				return
			}
			t.Error("must not create an existing user")
		case "/api/v1/preauthkey":
			json.NewDecoder(r.Body).Decode(&got)
			w.Write([]byte(`{"preAuthKey":{"id":"9","key":"abc","reusable":false,"ephemeral":true}}`))
		case "/api/v1/node":
			if r.URL.Query().Get("user") != "boxa" {
				t.Error("user filter")
			}
			w.Write([]byte(`{"nodes":[{"id":"1","name":"n1","online":true,"ipAddresses":["100.64.0.5"]}]}`))
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "k"}
	ctx := context.Background()
	if err := c.EnsureUser(ctx, "boxa"); err != nil {
		t.Fatal(err)
	}
	k, err := c.CreatePreAuthKey(ctx, "boxa", []string{TagHost}, false, true, 10*time.Minute)
	if err != nil || k.Key != "abc" || got["user"] != "boxa" || got["ephemeral"] != true {
		t.Fatalf("%v %+v %+v", err, k, got)
	}
	n, err := c.Nodes(ctx, "boxa")
	if err != nil || len(n) != 1 || !n[0].Online {
		t.Fatalf("%v %+v", err, n)
	}
	bad := &Client{BaseURL: srv.URL, APIKey: "x"}
	if _, err := bad.Nodes(ctx, ""); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal("auth error expected")
	}
	args := strings.Join(TailscaleUpArgs("https://m", "key", "host-1", []string{TagHost}), " ")
	if !strings.Contains(args, "--login-server=https://m") || !strings.Contains(args, "--advertise-tags=tag:boxa-host") {
		t.Fatal(args)
	}
}
