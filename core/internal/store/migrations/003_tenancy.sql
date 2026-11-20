-- v2 tenancy: users, workspaces, memberships, sessions, invites, waitlist,
-- password tokens. Booleans are INTEGER 0/1 and timestamps fixed-width TEXT
-- so the same DDL runs on SQLite and Postgres.
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  email_verified INTEGER NOT NULL DEFAULT 0,
  platform_admin INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS workspaces (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  owner_user_id TEXT NOT NULL DEFAULT '',
  max_boxes INTEGER NOT NULL DEFAULT 5,
  max_ttl_hours INTEGER NOT NULL DEFAULT 12,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS workspaces_owner ON workspaces(owner_user_id);
CREATE TABLE IF NOT EXISTS memberships (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  role TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL,
  UNIQUE(workspace_id, user_id)
);
CREATE INDEX IF NOT EXISTS memberships_user ON memberships(user_id);
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL DEFAULT '',
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS sessions_expires ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS invite_codes (
  id TEXT PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  max_uses INTEGER NOT NULL DEFAULT 1,
  uses INTEGER NOT NULL DEFAULT 0,
  expires_at TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS invite_redemptions (
  id TEXT PRIMARY KEY,
  invite_code_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE(invite_code_id, user_id)
);
CREATE INDEX IF NOT EXISTS invite_redemptions_user ON invite_redemptions(user_id);
CREATE TABLE IF NOT EXISTS waitlist (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL UNIQUE,
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS password_tokens (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  purpose TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TEXT NOT NULL,
  used_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS password_tokens_user ON password_tokens(user_id, purpose);
-- workspace scoping of existing entities (backfilled to "default" at startup)
ALTER TABLE boxes ADD COLUMN workspace_id TEXT;
CREATE INDEX IF NOT EXISTS boxes_workspace ON boxes(workspace_id);
ALTER TABLE api_keys ADD COLUMN workspace_id TEXT;
CREATE INDEX IF NOT EXISTS api_keys_workspace ON api_keys(workspace_id);
ALTER TABLE secrets ADD COLUMN workspace_id TEXT;
CREATE INDEX IF NOT EXISTS secrets_workspace ON secrets(workspace_id);
-- agents: name unique per workspace instead of globally (table rebuild)
CREATE TABLE agents_v3 (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL,
  UNIQUE(workspace_id, name)
);
INSERT INTO agents_v3(id, workspace_id, name, created_at, doc) SELECT id, '', name, created_at, doc FROM agents;
DROP TABLE agents;
ALTER TABLE agents_v3 RENAME TO agents;
CREATE INDEX IF NOT EXISTS agents_workspace ON agents(workspace_id)
