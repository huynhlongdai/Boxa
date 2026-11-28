package provider

// v2.3 (M29 phase B): Runpod — the first provider whose account balance
// Boxmes reads through the API (owner decision 2026-11-27, spec §12.8).
//
// There is no Runpod pod adapter yet: Implemented() is false, so the
// scheduler never places hosts here. The provider exists so platform admins
// can add Runpod accounts, validate their keys and follow balance, burn rate
// and runway (GPU capacity planned in spec M6).
//
// API facts (read 2026-11-28):
//   - GraphQL endpoint https://api.runpod.io/graphql. The GraphQL spec
//     (https://graphql-spec.runpod.io/) documents the header
//     "Authorization: Bearer <RUNPOD_API_KEY>"; the docs overview
//     (https://docs.runpod.io/sdks/graphql/configurations) shows the key as
//     ?api_key= instead. runpod-python sends the Bearer header, so Boxmes
//     does too (keeps the key out of URLs and proxy logs).
//   - User type (graphql-spec.runpod.io): clientBalance Float,
//     currentSpendPerHr Float, spendLimit Int, id String. Units are not
//     stated in the schema; docs.runpod.io/accounts-billing/billing says
//     balances are prepaid USD credits and the default spend limit is
//     "$80 per hour across all resources" → spendLimit is read as USD/hour
//     (UNVERIFIED against a live key; one third-party note calls it a
//     monthly cap).
//   - GraphQL is deprecated and "will be retired in early 2027"; REST API v2
//     has no balance endpoint ("Account balance (GraphQL only) … None. Keep
//     using the GraphQL API", docs.runpod.io/api-reference-v2/migrate-from-v1).
//     Watch for a v2 replacement before the retirement.
//   - Keys made after 2024-11-11 can be "Restricted"; a read-only GraphQL
//     key is enough for myself { … } (docs.runpod.io/get-started/api-keys).
//     Asking for fields a key may not read (e.g. clientLifetimeSpend)
//     returns an errors array next to data, so the query asks for nothing
//     else.
//   - Errors: GraphQL answers HTTP 200 with {"errors":[{"message":…}]} for
//     most failures; a bad key is expected as HTTP 401/403 or an errors
//     message mentioning authentication (exact shape UNVERIFIED without a
//     live key — both are handled).

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"boxmes/core/internal/model"
)

// RunpodGraphQL is the public GraphQL endpoint.
const RunpodGraphQL = "https://api.runpod.io/graphql"

// runpodBalanceQuery asks only for fields a read-only key may read.
const runpodBalanceQuery = "query { myself { id clientBalance currentSpendPerHr spendLimit } }"

// Runpod is the balance-only Runpod provider (no pod adapter yet).
type Runpod struct {
	URL string // GraphQL endpoint (tests point it at a fake server)
	c   *http.Client
}

func NewRunpod(c *http.Client) *Runpod { return &Runpod{URL: RunpodGraphQL, c: c} }

func (p *Runpod) ID() string        { return "runpod" }
func (p *Runpod) Name() string      { return "Runpod" }
func (p *Runpod) Tier() string      { return "T1" }
func (p *Runpod) Implemented() bool { return false }
func (p *Runpod) Caps() Caps        { return Caps{} }

func (p *Runpod) noAdapter() error {
	return model.EH(model.ErrProviderFailed, "Runpod accounts track balance and runway only; pods are not created by Boxmes yet.", "%s adapter not implemented yet", "Runpod")
}

type runpodMyself struct {
	ID                string   `json:"id"`
	ClientBalance     *float64 `json:"clientBalance"`
	CurrentSpendPerHr *float64 `json:"currentSpendPerHr"`
	SpendLimit        *float64 `json:"spendLimit"` // Int in the schema
}

func (p *Runpod) query(ctx context.Context, token string) (*runpodMyself, error) {
	var out struct {
		Data struct {
			Myself *runpodMyself `json:"myself"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	err := doJSON(ctx, p.c, "POST", p.URL, token, map[string]any{"query": runpodBalanceQuery}, &out)
	if err != nil {
		return nil, authErr(err, "Runpod")
	}
	if len(out.Errors) > 0 {
		var msgs []string
		for _, e := range out.Errors {
			msgs = append(msgs, e.Message)
		}
		msg := strings.Join(msgs, "; ")
		low := strings.ToLower(msg)
		if strings.Contains(low, "unauthor") || strings.Contains(low, "authenticat") || strings.Contains(low, "api key") || strings.Contains(low, "forbidden") || strings.Contains(low, "permission") {
			return nil, model.EH(model.ErrUnauthorized, "Create a Runpod API key with read access to GraphQL (Settings → API Keys) and paste it again.", "Runpod rejected the token: %s", msg)
		}
		// partial answer: keep the data when the balance is there
		if out.Data.Myself == nil || out.Data.Myself.ClientBalance == nil {
			return nil, model.E(model.ErrProviderFailed, "Runpod GraphQL error: %s", msg)
		}
	}
	if out.Data.Myself == nil {
		return nil, model.EH(model.ErrUnauthorized, "Create a Runpod API key with read access to GraphQL (Settings → API Keys) and paste it again.", "Runpod rejected the token: %s", "no account (myself is null)")
	}
	return out.Data.Myself, nil
}

// Validate reads myself: a key that can read the balance is a valid key.
func (p *Runpod) Validate(ctx context.Context, token string) error {
	_, err := p.query(ctx, token)
	return err
}

// ReadBalance implements BalanceReader.
func (p *Runpod) ReadBalance(ctx context.Context, token string) (Balance, error) {
	me, err := p.query(ctx, token)
	if err != nil {
		return Balance{}, err
	}
	if me.ClientBalance == nil {
		return Balance{}, model.E(model.ErrProviderFailed, "Runpod GraphQL error: %s", "clientBalance missing from the answer")
	}
	b := Balance{Currency: "USD", Balance: f64(*me.ClientBalance), ExternalID: me.ID, At: time.Now().UTC()}
	if me.CurrentSpendPerHr != nil {
		b.SpendPerHour = f64(*me.CurrentSpendPerHr)
	}
	if me.SpendLimit != nil && *me.SpendLimit > 0 {
		b.SpendLimitPerHour = f64(*me.SpendLimit)
	}
	return b, nil
}

func (p *Runpod) Offers(ctx context.Context, token string) ([]model.Offer, error) { return nil, nil }
func (p *Runpod) Create(ctx context.Context, token string, req CreateRequest) (Handle, error) {
	return Handle{}, p.noAdapter()
}
func (p *Runpod) Destroy(ctx context.Context, token, ref string) error { return p.noAdapter() }
func (p *Runpod) Status(ctx context.Context, token, ref string) (string, error) {
	return "unknown", fmt.Errorf("runpod: no pod adapter")
}
