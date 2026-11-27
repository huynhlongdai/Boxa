package provhealth

import (
	"errors"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"hetzner API error: HTTP 429: rate limit exceeded":                                                          ClassRateLimit,
		"E2B concurrency limit reached (429): You have reached the maximum number of concurrent E2B sandboxes (20)": ClassQuota, // M29-S
		"HTTP 429: maximum number of concurrent template builds (20)":                                               ClassQuota,
		"HTTP 401: unauthorized":                           ClassAuth,
		"vultr rejected the token (403)":                   ClassAuth,
		"HTTP 403: server_limit_exceeded quota":            ClassAuth, // auth wins on 403
		"HTTP 422: resource_limit_exceeded: server quota":  ClassQuota,
		"HTTP 412: resource_unavailable in fsn1":           ClassCapacity,
		"context deadline exceeded":                        ClassTimeout,
		"dial tcp: lookup api.hetzner.cloud: no such host": ClassNetwork,
		"HTTP 503: service unavailable":                    ClassCapacity, // "unavailable" → capacity is acceptable (transient either way)
		"HTTP 502: bad gateway":                            ClassServer,
		"HTTP 400: invalid server type":                    ClassInvalid,
		"something odd":                                    ClassUnknown,
	}
	for in, want := range cases {
		if got := Classify(errors.New(in)); got != want {
			t.Errorf("Classify(%q)=%s want %s", in, got, want)
		}
	}
	if Classify(nil) != ClassOK {
		t.Fatal("nil must be ok")
	}
	if Transient(ClassAuth) || Transient(ClassInvalid) || !Transient(ClassServer) {
		t.Fatal("transient classes wrong")
	}
}

func TestBreakerOpensHalfOpensAndCloses(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	tr := New(time.Hour, BreakerConfig{ConsecutiveFailures: 3, Cooldown: time.Minute})
	tr.SetClock(func() time.Time { return now })
	boom := errors.New("HTTP 500: internal server error")
	for i := 0; i < 2; i++ {
		tr.Record("hetzner", Sample{Kind: KindCreate}, boom, 0)
	}
	if ok, _ := tr.Allow("hetzner"); !ok {
		t.Fatal("2 failures must not open")
	}
	tr.Record("hetzner", Sample{Kind: KindCreate}, boom, 0)
	if ok, why := tr.Allow("hetzner"); ok || why == "" {
		t.Fatalf("3 failures must open: %v %s", ok, why)
	}
	st := tr.Stats("hetzner", "")
	if st.Status != "down" || st.Breaker.State != Open || st.CreateSuccessRate != 0 || st.ErrorClasses[ClassServer] != 3 {
		t.Fatalf("stats %+v", st)
	}
	// auth errors never trip the breaker
	tr.Record("vultr", Sample{Kind: KindAPI}, errors.New("HTTP 401: unauthorized"), 0)
	tr.Record("vultr", Sample{Kind: KindAPI}, errors.New("HTTP 401: unauthorized"), 0)
	tr.Record("vultr", Sample{Kind: KindAPI}, errors.New("HTTP 401: unauthorized"), 0)
	if ok, _ := tr.Allow("vultr"); !ok {
		t.Fatal("auth errors must not open the breaker")
	}
	// cooldown → half-open: exactly one probe
	now = now.Add(61 * time.Second)
	if ok, _ := tr.Allow("hetzner"); !ok {
		t.Fatal("half-open probe must pass")
	}
	if ok, _ := tr.Allow("hetzner"); ok {
		t.Fatal("second call during probe must be refused")
	}
	// failed probe re-opens with doubled cooldown
	tr.Record("hetzner", Sample{Kind: KindCreate}, boom, 0)
	st = tr.Stats("hetzner", "")
	if st.Breaker.State != Open || st.Breaker.OpenUntil.Sub(now) != 2*time.Minute {
		t.Fatalf("re-open backoff: %+v", st.Breaker)
	}
	now = now.Add(121 * time.Second)
	tr.Allow("hetzner")
	tr.Record("hetzner", Sample{Kind: KindCreate, LatencyMS: 900}, nil, 0)
	if st := tr.Stats("hetzner", ""); st.Breaker.State != Closed || st.Breaker.Opens != 0 {
		t.Fatalf("success must close: %+v", st.Breaker)
	}
}

func TestRateLimitAndPercentiles(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	tr := New(time.Hour, BreakerConfig{})
	tr.SetClock(func() time.Time { return now })
	tr.Record("do", Sample{Kind: KindAPI}, errors.New("HTTP 429: too many requests"), 20*time.Second)
	if ok, why := tr.Allow("do"); ok || why == "" {
		t.Fatal("429 must block")
	}
	now = now.Add(21 * time.Second)
	if ok, _ := tr.Allow("do"); !ok {
		t.Fatal("block must expire")
	}
	for i, ms := range []float64{100, 200, 300, 400, 1000} {
		tr.Record("do", Sample{Kind: KindAPI, LatencyMS: ms, Region: "sgp1"}, nil, 0)
		tr.Record("do", Sample{Kind: KindProvision, LatencyMS: float64(i+1) * 10000, Region: "sgp1"}, nil, 0)
	}
	st := tr.Stats("do", "sgp1")
	if st.APILatencyP50MS != 300 || st.APILatencyP95MS != 880 || st.ProvisionP50S != 30 || st.ProvisionP95S != 48 {
		t.Fatalf("percentiles %+v", st)
	}
	// persistence round trip
	snap := tr.Export()
	tr2 := New(time.Hour, BreakerConfig{})
	tr2.SetClock(func() time.Time { return now })
	tr2.Import(snap)
	if tr2.Stats("do", "").Samples != tr.Stats("do", "").Samples {
		t.Fatal("import lost samples")
	}
	tr2.Reset("do")
	if st := tr2.Stats("do", ""); st.RateLimit.Hits != 0 {
		t.Fatal("reset")
	}
	if Percentile(nil, 50) != 0 || Percentile([]float64{7}, 95) != 7 {
		t.Fatal("edge percentiles")
	}
}
