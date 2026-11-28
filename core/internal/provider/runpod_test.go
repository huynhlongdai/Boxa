package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"boxmes/core/internal/model"
	"boxmes/core/internal/provhealth"
)

// fakeRunpod answers the GraphQL myself query the way graphql-spec.runpod.io
// documents it; the answer depends on the Bearer key.
func fakeRunpod(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.URL.Query().Get("api_key") != "" {
			t.Errorf("request %s %s ct=%q", r.Method, r.URL, r.Header.Get("Content-Type"))
		}
		b, _ := io.ReadAll(r.Body)
		var q struct{ Query string }
		json.Unmarshal(b, &q)
		if !strings.Contains(q.Query, "myself") || !strings.Contains(q.Query, "clientBalance") || strings.Contains(q.Query, "clientLifetimeSpend") {
			t.Errorf("query %q", q.Query)
		}
		switch r.Header.Get("Authorization") {
		case "Bearer good":
			w.Write([]byte(`{"data":{"myself":{"id":"user_abc","clientBalance":42.5,"currentSpendPerHr":0.74,"spendLimit":80}}}`))
		case "Bearer idle":
			w.Write([]byte(`{"data":{"myself":{"id":"user_idle","clientBalance":1.0080424408,"currentSpendPerHr":0,"spendLimit":80}}}`))
		case "Bearer partial":
			w.Write([]byte(`{"data":{"myself":{"id":"user_p","clientBalance":7,"currentSpendPerHr":null,"spendLimit":null}},"errors":[{"message":"field x not allowed"}]}`))
		case "Bearer gqlauth":
			w.Write([]byte(`{"data":{"myself":null},"errors":[{"message":"Unauthorized: invalid api key"}]}`))
		case "Bearer nulluser":
			w.Write([]byte(`{"data":{"myself":null}}`))
		case "Bearer boom":
			w.WriteHeader(502)
			w.Write([]byte(`bad gateway`))
		default:
			w.WriteHeader(401)
			w.Write([]byte(`{"errors":[{"message":"Unauthorized"}]}`))
		}
	}))
}

func TestRunpodBalance(t *testing.T) {
	srv := fakeRunpod(t)
	defer srv.Close()
	p := NewRunpod(srv.Client())
	p.URL = srv.URL
	ctx := context.Background()
	if p.Implemented() || p.ID() != "runpod" {
		t.Fatal("runpod is balance-only")
	}
	var _ BalanceReader = p
	b, err := p.ReadBalance(ctx, "good")
	if err != nil || b.Balance == nil || *b.Balance != 42.5 || b.SpendPerHour == nil || *b.SpendPerHour != 0.74 ||
		b.SpendLimitPerHour == nil || *b.SpendLimitPerHour != 80 || b.Currency != "USD" || b.ExternalID != "user_abc" || b.At.IsZero() {
		t.Fatalf("good %+v %v", b, err)
	}
	if err := p.Validate(ctx, "good"); err != nil {
		t.Fatal(err)
	}
	b, err = p.ReadBalance(ctx, "idle")
	if err != nil || *b.SpendPerHour != 0 || *b.Balance < 1 {
		t.Fatalf("idle %+v %v", b, err)
	}
	// a partial answer keeps the balance
	b, err = p.ReadBalance(ctx, "partial")
	if err != nil || *b.Balance != 7 || b.SpendPerHour != nil || b.SpendLimitPerHour != nil {
		t.Fatalf("partial %+v %v", b, err)
	}
	for _, tok := range []string{"bad", "gqlauth", "nulluser"} {
		_, err := p.ReadBalance(ctx, tok)
		if err == nil || provhealth.Classify(err) != provhealth.ClassAuth {
			t.Fatalf("%s: %v (%s)", tok, err, provhealth.Classify(err))
		}
		if ae, ok := err.(*model.APIError); !ok || ae.Code != model.ErrUnauthorized {
			t.Fatalf("%s: %#v", tok, err)
		}
	}
	if _, err := p.ReadBalance(ctx, "boom"); err == nil || provhealth.Classify(err) != provhealth.ClassServer {
		t.Fatalf("502: %v", err)
	}
	if _, err := p.Create(ctx, "good", CreateRequest{}); err == nil {
		t.Fatal("runpod has no pod adapter")
	}
	if TokenFromEnv("runpod", func(k string) string {
		if k == "RUNPOD_API_KEY" {
			return "rk"
		}
		return ""
	}) != "rk" {
		t.Fatal("env token")
	}
}
