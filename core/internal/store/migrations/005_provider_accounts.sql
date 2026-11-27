-- v2.2 (M29 phase A): provider accounts. A provider can hold several API
-- credentials (accounts), hosts and ledger rows remember the account.
-- Existing single tokens become the default account of their provider
-- (pa_<provider>_default) in Go at startup (manager.migrateAccounts), since
-- the token lives inside the providers JSON doc.
CREATE TABLE IF NOT EXISTS provider_accounts (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  owner_workspace_id TEXT,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS provider_accounts_provider ON provider_accounts(provider);
CREATE TABLE IF NOT EXISTS provider_account_snapshots (
  account_id TEXT NOT NULL,
  ts TEXT NOT NULL,
  doc TEXT NOT NULL,
  PRIMARY KEY (account_id, ts)
);
ALTER TABLE ledger ADD COLUMN account_id TEXT;
CREATE INDEX IF NOT EXISTS ledger_account ON ledger(account_id, ts)
