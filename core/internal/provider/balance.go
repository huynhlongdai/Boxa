package provider

// v2.3 (M29 phase B): reading an account's money at the provider.

import (
	"context"
	"time"
)

// Balance is one reading of a provider account's money, as the provider's
// API reports it. Nil fields are not reported by that provider. Amounts are
// in Currency (the provider's billing currency).
type Balance struct {
	Currency string `json:"currency"`
	// Balance is the money left (prepaid credit); positive = money left.
	Balance *float64 `json:"balance,omitempty"`
	// SpendPerHour is the provider's own current burn rate (Runpod
	// currentSpendPerHr), in Currency per hour.
	SpendPerHour *float64 `json:"spend_per_hour,omitempty"`
	// SpendLimitPerHour is the provider-side spend ceiling per hour (Runpod
	// spendLimit, default $80/h).
	SpendLimitPerHour *float64 `json:"spend_limit_per_hour,omitempty"`
	PendingCharges    *float64 `json:"pending_charges,omitempty"`
	MonthToDate       *float64 `json:"month_to_date,omitempty"`
	// ExternalID is the provider-side account id when the reading carries
	// it (Runpod myself.id), so rotation checks can use it.
	ExternalID string    `json:"external_id,omitempty"`
	At         time.Time `json:"at"`
}

// BalanceReader is implemented by providers whose API reports an account's
// balance (spec §5 AccountReader, phase B). token is the account's
// read-only billing key when one is set, else its main key. Providers
// without it keep manual / estimated balances.
type BalanceReader interface {
	ReadBalance(ctx context.Context, token string) (Balance, error)
}

func f64(v float64) *float64 { return &v }
