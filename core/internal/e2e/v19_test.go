package e2e

// v1.9: readable gateway URLs. https://<name>.<domain>/ serves the primary
// port, https://<name>-<label>.<domain>/ the others; names are unique (word
// suffix on a derived clash, explicit clash refused with a suggestion),
// reserved names refused, renames and labels via PATCH / PUT, and the legacy
// <key>-<port> alias keeps working.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"boxaz/core/internal/client"
	"boxaz/core/internal/manager"
	"boxaz/core/internal/model"
	"boxaz/core/internal/sandbox"
)

func TestGatewayReadableNames(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash required")
	}
	prev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("E2B-Traffic-Access-Token")
		if !strings.HasPrefix(tok, "tat-sb") {
			http.Error(w, "missing traffic access token", http.StatusForbidden)
			return
		}
		io.WriteString(w, "hello from "+strings.TrimPrefix(tok, "tat-"))
	}))
	t.Cleanup(prev.Close)
	fk := &privSandbox{deploySandbox: &deploySandbox{dir: t.TempDir(), state: map[string]string{}}, preview: prev.URL, private: map[string]bool{}}
	reg := sandbox.DefaultRegistry(nil)
	reg.Add(sandbox.AsProvider(fk))
	e := setupReg(t, reg, func(c *manager.Config) {
		c.AutoSandbox = true
		c.ProviderChain = []string{"fakepriv"}
		c.HealthInterval = time.Hour
		c.SandboxEmptyAfter = 300 * time.Millisecond
		c.GatewayDomain = "example.test"
	}, 0)
	e.m.Cfg.PublicURL = "https://gw.example.test"
	ag, _ := e.agentClient("names-agent", 0)
	e.must(e.admin, "PUT", "/providers/fakepriv/token", map[string]any{"token": "k"}, nil)
	req := map[string]any{"cpu": 1, "ram_mb": 1024, "disk_gb": 5, "isolation": "sandbox"}

	// 1. box "demo-app": primary port 8000 at the bare name, others by label
	var b1 model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "demo-app", "lifecycle": "session", "ports": []int{8000, 5432, 7777}, "requirements": req}, &b1)
	got := waitStatus(t, e.m, b1.ID, model.StatusRunning, 20*time.Second)
	if got.Subdomain != "demo-app" || got.URL != "https://demo-app.example.test/" {
		t.Fatalf("box name %q url %q", got.Subdomain, got.URL)
	}
	var pr manager.PortsResult
	e.must(ag, "GET", "/boxes/"+b1.ID+"/ports", nil, &pr)
	want := map[int][2]string{8000: {"web", "https://demo-app.example.test/"}, 5432: {"db", "https://demo-app-db.example.test/"}, 7777: {"alpha", "https://demo-app-alpha.example.test/"}}
	if pr.Name != "demo-app" || pr.PrimaryPort != 8000 || len(pr.Ports) != 3 {
		t.Fatalf("ports %+v", pr)
	}
	for _, v := range pr.Ports {
		w := want[v.Port]
		if v.Label != w[0] || v.URL != w[1] || v.GatewayURL != w[1] || v.Primary != (v.Port == 8000) || !strings.HasSuffix(v.AliasURL, "-"+strconv.Itoa(v.Port)+".example.test/") {
			t.Fatalf("port %d view %+v", v.Port, v)
		}
		if strings.ContainsAny(strings.TrimSuffix(strings.TrimPrefix(v.URL, "https://"), ".example.test/"), "0123456789") {
			t.Fatalf("number in host %q", v.URL)
		}
	}
	for _, hh := range []string{"demo-app.example.test", "demo-app-db.example.test", "demo-app-alpha.example.test", "DEMO-APP.example.test"} {
		if st, body := hostDo(t, e.srv.URL, hh, "/", nil); st != 200 || !strings.HasPrefix(body, "hello from sb") {
			t.Fatalf("%s → %d %q", hh, st, body)
		}
	}
	alias := strings.TrimSuffix(strings.TrimPrefix(pr.Ports[0].AliasURL, "https://"), "/")
	if st, _ := hostDo(t, e.srv.URL, alias, "/", nil); st != 200 {
		t.Fatalf("alias %s → %d", alias, st)
	}
	for _, hh := range []string{"demo-app-nope.example.test", "demo-app-8000.example.test", "hark.example.test"} {
		if st, _ := hostDo(t, e.srv.URL, hh, "/", nil); st != 404 {
			t.Fatalf("%s → %d", hh, st)
		}
	}

	// 2. label a port; duplicate / numeric labels are refused
	var pv manager.PortView
	e.must(ag, "PUT", "/boxes/"+b1.ID+"/ports/7777", map[string]any{"label": "api"}, &pv)
	if pv.Label != "api" || pv.URL != "https://demo-app-api.example.test/" {
		t.Fatalf("labelled %+v", pv)
	}
	if st, _ := hostDo(t, e.srv.URL, "demo-app-api.example.test", "/", nil); st != 200 {
		t.Fatalf("label host → %d", st)
	}
	if st, _ := hostDo(t, e.srv.URL, "demo-app-alpha.example.test", "/", nil); st != 404 {
		t.Fatalf("old label host → %d", st)
	}
	expectErr(t, e, ag, "PUT", "/boxes/"+b1.ID+"/ports/7777", map[string]any{"label": "db"}, "already used by port 5432")
	expectErr(t, e, ag, "PUT", "/boxes/"+b1.ID+"/ports/7777", map[string]any{"label": "9000"}, "invalid port label")
	// expose with a label
	var er manager.PortsResult
	e.must(ag, "POST", "/boxes/"+b1.ID+"/ports", map[string]any{"port": 9229, "label": "inspect"}, &er)
	if er.Ports[0].Port != 9229 || er.Ports[0].URL != "https://demo-app-inspect.example.test/" {
		t.Fatalf("expose label %+v", er.Ports[0])
	}

	// 3. a second box asking for the same slug gets a word suffix (no digits)
	var b2 model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "slug": "demo-app", "lifecycle": "session", "ports": []int{3000}, "requirements": req}, &b2)
	got2 := waitStatus(t, e.m, b2.ID, model.StatusRunning, 20*time.Second)
	if got2.Subdomain != "demo-app-blue" || got2.URL != "https://demo-app-blue.example.test/" {
		t.Fatalf("second box name %q url %q", got2.Subdomain, got2.URL)
	}
	// explicit name that is taken → refused, next suggests a free one
	ae := expectErr(t, e, ag, "PATCH", "/boxes/"+b2.ID, map[string]any{"subdomain": "demo-app"}, "is taken")
	if len(ae.Next) != 1 || !strings.Contains(ae.Next[0], "--name demo-app-") {
		t.Fatalf("next %+v", ae.Next)
	}
	// a name that collides with another box's labelled host → refused
	expectErr(t, e, ag, "PATCH", "/boxes/"+b2.ID, map[string]any{"subdomain": "demo-app-db"}, "is taken")
	// reserved and invalid names → refused
	expectErr(t, e, ag, "PATCH", "/boxes/"+b2.ID, map[string]any{"subdomain": "hark"}, "reserved")
	expectErr(t, e, ag, "PATCH", "/boxes/"+b2.ID, map[string]any{"subdomain": "8000"}, "invalid box name")
	// rename → new host answers, old one is gone, alias unchanged
	var rb model.Box
	e.must(ag, "PATCH", "/boxes/"+b2.ID, map[string]any{"subdomain": "Shop"}, &rb)
	if rb.Subdomain != "shop" || rb.URL != "https://shop.example.test/" {
		t.Fatalf("renamed %q %q", rb.Subdomain, rb.URL)
	}
	if st, _ := hostDo(t, e.srv.URL, "shop.example.test", "/", nil); st != 200 {
		t.Fatalf("renamed host → %d", st)
	}
	if st, _ := hostDo(t, e.srv.URL, "demo-app-blue.example.test", "/", nil); st != 404 {
		t.Fatalf("old name → %d", st)
	}
	// explicit subdomain at create time
	var b3 model.Box
	e.must(ag, "POST", "/boxes", map[string]any{"template": "base", "subdomain": "Tiệm Phở", "lifecycle": "session", "ports": []int{8080}, "requirements": req}, &b3)
	got3 := waitStatus(t, e.m, b3.ID, model.StatusRunning, 20*time.Second)
	if got3.Subdomain != "tiem-pho" || got3.URL != "https://tiem-pho.example.test/" {
		t.Fatalf("explicit create %q %q", got3.Subdomain, got3.URL)
	}
	expectErr(t, e, ag, "POST", "/boxes", map[string]any{"template": "base", "subdomain": "shop", "lifecycle": "session", "ports": []int{8080}, "requirements": req}, "is taken")

	// 4. gateway info shows the patterns
	var gi manager.GatewayInfo
	e.must(e.admin, "GET", "/gateway", nil, &gi)
	if gi.URLPattern != "https://<name>.example.test/" || gi.LabelPattern != "https://<name>-<label>.example.test/" {
		t.Fatalf("gateway info %+v", gi)
	}
	// 5. box down frees the name
	e.must(ag, "DELETE", "/boxes/"+b2.ID, nil, nil)
	waitStatus(t, e.m, b2.ID, model.StatusTerminated, 20*time.Second)
	if st, _ := hostDo(t, e.srv.URL, "shop.example.test", "/", nil); st != 404 {
		t.Fatalf("down box name → %d", st)
	}
	e.must(ag, "PATCH", "/boxes/"+b3.ID, map[string]any{"subdomain": "shop"}, &rb)
	if rb.Subdomain != "shop" {
		t.Fatalf("name not freed: %q", rb.Subdomain)
	}
	for _, id := range []string{b1.ID, b3.ID} {
		e.must(ag, "DELETE", "/boxes/"+id, nil, nil)
	}
}

// expectErr asserts an API error whose message contains want.
func expectErr(t *testing.T, e *env, c *client.Client, method, path string, body any, want string) *client.APIError {
	t.Helper()
	err := c.JSON(e.ctx, method, path, body, nil)
	ae, ok := err.(*client.APIError)
	if !ok || !strings.Contains(ae.Message, want) {
		t.Fatalf("%s %s: want error containing %q, got %v", method, path, want, err)
	}
	return ae
}
