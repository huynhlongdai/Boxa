package store

// v2.2 provider accounts (M29 phase A).

import (
	"context"

	"boxmes/core/internal/model"
)

// Accounts is the provider-account part of the store.
type Accounts interface {
	PutProviderAccount(ctx context.Context, a *model.ProviderAccount) error
	GetProviderAccount(ctx context.Context, id string) (*model.ProviderAccount, error)
	ListProviderAccounts(ctx context.Context, provider string) ([]*model.ProviderAccount, error)
	DeleteProviderAccount(ctx context.Context, id string) error
	// SetLedgerAccount stamps account_id on a box's ledger rows written
	// since from that carry none yet (migration backfill).
	SetLedgerAccount(ctx context.Context, boxID, accountID string, from string) error
}

// accountDoc keeps the encrypted credentials (json:"-" on the model).
type accountDoc struct {
	model.ProviderAccount
	CredentialEnc        string `json:"credential_enc,omitempty"`
	BillingCredentialEnc string `json:"billing_credential_enc,omitempty"`
}

func unAccount(d *accountDoc) *model.ProviderAccount {
	a := d.ProviderAccount
	a.CredentialEnc, a.BillingCredentialEnc = d.CredentialEnc, d.BillingCredentialEnc
	a.Stats = nil
	return &a
}

func (s *SQL) PutProviderAccount(ctx context.Context, a *model.ProviderAccount) error {
	c := *a
	c.Stats = nil
	return s.putDoc(ctx, "provider_accounts", a.ID, a.CreatedAt, map[string]any{"provider": a.Provider, "owner_workspace_id": nullStr(a.OwnerWorkspaceID)},
		accountDoc{c, a.CredentialEnc, a.BillingCredentialEnc})
}

func (s *SQL) GetProviderAccount(ctx context.Context, id string) (*model.ProviderAccount, error) {
	d, err := getDoc[accountDoc](ctx, s, "provider_accounts", "id=?", id)
	if err != nil {
		return nil, err
	}
	return unAccount(d), nil
}

func (s *SQL) ListProviderAccounts(ctx context.Context, provider string) ([]*model.ProviderAccount, error) {
	where, args := "", []any{}
	if provider != "" {
		where, args = "provider=?", []any{provider}
	}
	ds, err := listDocs[accountDoc](ctx, s, "provider_accounts", where, "created_at, id", args...)
	if err != nil {
		return nil, err
	}
	out := make([]*model.ProviderAccount, 0, len(ds))
	for _, d := range ds {
		out = append(out, unAccount(d))
	}
	return out, nil
}

func (s *SQL) DeleteProviderAccount(ctx context.Context, id string) error {
	return s.exec(ctx, "DELETE FROM provider_accounts WHERE id=?", id)
}

func (s *SQL) SetLedgerAccount(ctx context.Context, boxID, accountID, from string) error {
	return s.exec(ctx, "UPDATE ledger SET account_id=? WHERE box_id=? AND ts>=? AND (account_id IS NULL OR account_id='')", accountID, boxID, from)
}
