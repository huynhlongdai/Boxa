package store

// v2.2 provider accounts (M29 phase A).

import (
	"context"
	"encoding/json"
	"strings"
	"time"

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

	// v2.3 (M29 phase B): balance history (provider_account_snapshots).
	PutAccountSnapshot(ctx context.Context, s *model.AccountSnapshot) error
	// ListAccountSnapshots returns the snapshots of the accounts in
	// [from, to) oldest first (to zero = now).
	ListAccountSnapshots(ctx context.Context, accountIDs []string, from, to time.Time) ([]*model.AccountSnapshot, error)
	// PruneAccountSnapshots deletes snapshots older than before.
	PruneAccountSnapshots(ctx context.Context, before time.Time) error
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

func (s *SQL) PutAccountSnapshot(ctx context.Context, sn *model.AccountSnapshot) error {
	b, err := json.Marshal(sn)
	if err != nil {
		return err
	}
	return s.exec(ctx, "INSERT INTO provider_account_snapshots (account_id, ts, doc) VALUES (?,?,?) ON CONFLICT(account_id, ts) DO UPDATE SET doc=excluded.doc",
		sn.AccountID, FmtTime(sn.TS), string(b))
}

func (s *SQL) ListAccountSnapshots(ctx context.Context, ids []string, from, to time.Time) ([]*model.AccountSnapshot, error) {
	out := []*model.AccountSnapshot{}
	if len(ids) == 0 {
		return out, nil
	}
	if to.IsZero() {
		to = time.Now().Add(time.Minute)
	}
	args := []any{}
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, FmtTime(from), FmtTime(to))
	q := "SELECT doc FROM provider_account_snapshots WHERE account_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ") AND ts>=? AND ts<? ORDER BY ts, account_id"
	rows, err := s.db.QueryContext(ctx, s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var v model.AccountSnapshot
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			continue
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

func (s *SQL) PruneAccountSnapshots(ctx context.Context, before time.Time) error {
	return s.exec(ctx, "DELETE FROM provider_account_snapshots WHERE ts<?", FmtTime(before))
}
