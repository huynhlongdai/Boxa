package sandbox

import (
	"os"
	"testing"
)

// TestPrizedLive makes free, read-only calls against the real Prized API
// (no sandbox is created). Opt-in: BOXA_LIVE_PRIZED=1 PRIZED_API_KEY=dcp_….
func TestPrizedLive(t *testing.T) {
	if os.Getenv("BOXA_LIVE_PRIZED") != "1" {
		t.Skip("set BOXA_LIVE_PRIZED=1 and PRIZED_API_KEY to run")
	}
	d := NewPrized(nil, "")
	c := Cred{"token": os.Getenv("PRIZED_API_KEY")}
	if err := d.Validate(bg, c); err != nil {
		t.Fatalf("validate (GET /sandbox-templates): %v", err)
	}
	l, err := d.List(bg, c)
	if err != nil {
		t.Fatalf("list (GET /sandboxes): %v", err)
	}
	t.Logf("prized live: templates OK, %d sandbox(es) listed", len(l))
}
