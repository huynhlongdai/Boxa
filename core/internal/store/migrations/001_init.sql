-- Portable schema (SQLite + Postgres). Timestamps are fixed-width UTC TEXT
-- (2006-01-02T15:04:05.000000Z) so they sort lexicographically in both engines.
-- Entities keep a few indexed columns plus a JSON document column `doc`.
CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS api_keys (
  id TEXT PRIMARY KEY,
  agent_id TEXT,
  hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS boxes (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  status TEXT NOT NULL,
  lifecycle TEXT NOT NULL,
  provider TEXT NOT NULL,
  host_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS boxes_agent ON boxes(agent_id);
CREATE INDEX IF NOT EXISTS boxes_status ON boxes(status);
CREATE TABLE IF NOT EXISTS hosts (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  status TEXT NOT NULL,
  token_hash TEXT NOT NULL,
  bootstrap_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS providers (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS offers (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  source TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS offers_provider ON offers(provider);
CREATE TABLE IF NOT EXISTS price_history (
  id TEXT PRIMARY KEY,
  offer_id TEXT NOT NULL,
  ts TEXT NOT NULL,
  price_hour DOUBLE PRECISION NOT NULL,
  price_month DOUBLE PRECISION NOT NULL
);
CREATE INDEX IF NOT EXISTS price_history_offer ON price_history(offer_id, ts);
CREATE TABLE IF NOT EXISTS capabilities (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  box_id TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS jobs_box ON jobs(box_id);
CREATE TABLE IF NOT EXISTS events (
  id TEXT PRIMARY KEY,
  ts TEXT NOT NULL,
  actor TEXT NOT NULL,
  box_id TEXT,
  agent_id TEXT,
  type TEXT NOT NULL,
  message TEXT NOT NULL,
  cost_usd DOUBLE PRECISION
);
CREATE INDEX IF NOT EXISTS events_ts ON events(ts);
CREATE INDEX IF NOT EXISTS events_box ON events(box_id, ts);
CREATE TABLE IF NOT EXISTS ledger (
  id TEXT PRIMARY KEY,
  ts TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  box_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  kind TEXT NOT NULL,
  billing TEXT NOT NULL,
  amount_usd DOUBLE PRECISION NOT NULL,
  note TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ledger_ts ON ledger(ts);
CREATE INDEX IF NOT EXISTS ledger_agent ON ledger(agent_id, ts);
CREATE INDEX IF NOT EXISTS ledger_box ON ledger(box_id);
CREATE TABLE IF NOT EXISTS templates (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS secrets (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS snapshots (
  id TEXT PRIMARY KEY,
  box_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS snapshots_box ON snapshots(box_id);
CREATE TABLE IF NOT EXISTS approvals (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  dedupe_key TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS approvals_dedupe ON approvals(dedupe_key);
CREATE TABLE IF NOT EXISTS idempotency (
  id TEXT PRIMARY KEY,
  box_id TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS logs (
  id TEXT PRIMARY KEY,
  box_id TEXT NOT NULL,
  host_id TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS kv (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
