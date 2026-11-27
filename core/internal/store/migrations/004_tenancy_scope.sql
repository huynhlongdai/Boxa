-- v2.0 tenancy: workspace scope for events, ledger, approvals and templates,
-- workspace invites. Rows written before v2.0 belonged to the single
-- operator, so they move into the default workspace exactly once (here);
-- later rows without a workspace are platform-wide on purpose.
ALTER TABLE events ADD COLUMN workspace_id TEXT;
UPDATE events SET workspace_id='ws_default' WHERE workspace_id IS NULL;
CREATE INDEX IF NOT EXISTS events_workspace ON events(workspace_id, ts);
ALTER TABLE ledger ADD COLUMN workspace_id TEXT;
UPDATE ledger SET workspace_id='ws_default' WHERE workspace_id IS NULL;
CREATE INDEX IF NOT EXISTS ledger_workspace ON ledger(workspace_id, ts);
ALTER TABLE approvals ADD COLUMN workspace_id TEXT;
UPDATE approvals SET workspace_id='ws_default' WHERE workspace_id IS NULL;
CREATE INDEX IF NOT EXISTS approvals_workspace ON approvals(workspace_id);
ALTER TABLE templates ADD COLUMN workspace_id TEXT;
UPDATE templates SET workspace_id='ws_default' WHERE workspace_id IS NULL AND doc NOT LIKE '%"builtin":true%';
UPDATE templates SET workspace_id='' WHERE workspace_id IS NULL;
CREATE INDEX IF NOT EXISTS templates_workspace ON templates(workspace_id);
CREATE TABLE IF NOT EXISTS workspace_invites (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  email TEXT NOT NULL,
  role TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TEXT NOT NULL,
  accepted_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS workspace_invites_ws ON workspace_invites(workspace_id);
CREATE INDEX IF NOT EXISTS workspace_invites_email ON workspace_invites(email)
