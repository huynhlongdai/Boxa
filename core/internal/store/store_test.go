package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"boxa/core/internal/model"
)

func TestSQLiteRoundTrip(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	// migrations are idempotent
	if err := s.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	b := &model.Box{ID: "box_1", Slug: "s1", AgentID: "agt_1", Status: model.StatusRunning, Lifecycle: "task", Provider: "local", HostID: "h1", CreatedAt: time.Now().UTC()}
	if err := s.PutBox(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBox(ctx, "s1") // by slug
	if err != nil || got.ID != "box_1" {
		t.Fatalf("by slug: %v %v", got, err)
	}
	b.Status = model.StatusTerminated
	s.PutBox(ctx, b)
	if l, _ := s.ListBoxes(ctx, BoxFilter{}); len(l) != 0 {
		t.Fatalf("terminated listed: %d", len(l))
	}
	if l, _ := s.ListBoxes(ctx, BoxFilter{IncludeTerminated: true}); len(l) != 1 {
		t.Fatal("include terminated")
	}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		s.AddLedger(ctx, &model.LedgerEntry{ID: "l" + string(rune('a'+i)), TS: now, AgentID: "agt_1", BoxID: "box_1", Provider: "local", Kind: "compute", Billing: "pool", AmountUSD: 0.5})
	}
	sum, _ := s.SumLedger(ctx, LedgerFilter{AgentID: "agt_1", From: now.Add(-time.Minute)})
	if sum != 1.5 {
		t.Fatalf("sum %v", sum)
	}
	by, _ := s.SumLedgerBy(ctx, LedgerFilter{}, "billing")
	if by["pool"] != 1.5 {
		t.Fatalf("by %v", by)
	}
	if _, err := s.SumLedgerBy(ctx, LedgerFilter{}, "note; DROP TABLE x"); err == nil {
		t.Fatal("group column must be whitelisted")
	}
	o := model.Offer{Provider: "hetzner", SKU: "cx22", Region: "fsn1", PriceHour: 0.006, Source: "battleships"}
	s.ReplaceOffers(ctx, "hetzner", "battleships", []model.Offer{o})
	o.PriceHour = 0.007
	s.ReplaceOffers(ctx, "hetzner", "battleships", []model.Offer{o})
	s.ReplaceOffers(ctx, "hetzner", "battleships", []model.Offer{o})
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM price_history").Scan(&n)
	if n != 2 {
		t.Fatalf("price history rows %d (want 2: initial + change)", n)
	}
	offers, _ := s.ListOffers(ctx, "hetzner")
	if len(offers) != 1 || offers[0].PriceHour != 0.007 {
		t.Fatalf("offers %+v", offers)
	}
	h := &model.Host{ID: "h1", Provider: "local", Status: "ready", TokenHash: "abc", CreatedAt: now}
	s.PutHost(ctx, h)
	if hh, err := s.GetHostByToken(ctx, "abc"); err != nil || hh.TokenHash != "abc" {
		t.Fatalf("host by token %v %v", hh, err)
	}
	if _, err := s.GetHostByToken(ctx, ""); err == nil {
		t.Fatal("empty token must not match")
	}
}

func TestRebind(t *testing.T) {
	s := &SQL{dialect: "postgres"}
	if got := s.rebind("a=? AND b=?"); got != "a=$1 AND b=$2" {
		t.Fatal(got)
	}
}
