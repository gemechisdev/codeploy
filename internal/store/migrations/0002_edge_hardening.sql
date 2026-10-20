-- +goose Up
CREATE TABLE domains (
  id TEXT PRIMARY KEY,
  hostname TEXT NOT NULL UNIQUE,
  resource_id TEXT,
  kind TEXT NOT NULL DEFAULT 'resource' CHECK (kind IN ('panel','resource')),
  force_https INTEGER NOT NULL DEFAULT 1,
  redirect_www INTEGER NOT NULL DEFAULT 0,
  upstream TEXT NOT NULL DEFAULT '',
  cert_status TEXT NOT NULL DEFAULT 'issuing' CHECK (cert_status IN ('issuing','issued','error')),
  cert_expires_at TEXT,
  cert_error TEXT,
  issuer TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
-- At most one panel domain exists at a time (the panel-domain transition replaces it atomically).
CREATE UNIQUE INDEX domains_one_panel ON domains(kind) WHERE kind = 'panel';
CREATE TABLE edge_snippets (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, body TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE edge_state (
  id INTEGER PRIMARY KEY CHECK (id = 1), last_hash TEXT NOT NULL DEFAULT '', last_reconciled_at TEXT
);
INSERT OR IGNORE INTO edge_state(id) VALUES (1);
CREATE TABLE hardening_runs (
  id TEXT PRIMARY KEY, mode TEXT NOT NULL CHECK (mode IN ('audit','plan','apply')),
  started_at TEXT NOT NULL, finished_at TEXT, actor_id TEXT NOT NULL, score INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE hardening_results (
  run_id TEXT NOT NULL REFERENCES hardening_runs(id), check_key TEXT NOT NULL, tier INTEGER NOT NULL,
  status TEXT NOT NULL, detail TEXT NOT NULL, PRIMARY KEY (run_id, check_key)
);
CREATE TABLE hardening_changes (
  id TEXT PRIMARY KEY, run_id TEXT NOT NULL, check_key TEXT NOT NULL, tier INTEGER NOT NULL,
  snapshot_path TEXT NOT NULL, revert_unit TEXT, params TEXT NOT NULL DEFAULT '{}',
  applied_at TEXT NOT NULL, confirm_deadline TEXT, confirmed_at TEXT, reverted_at TEXT,
  state TEXT NOT NULL CHECK (state IN ('applied','pending_confirm','confirmed','reverting','reverted'))
);
CREATE INDEX hardening_changes_key ON hardening_changes(check_key, applied_at);

-- +goose Down
DROP TABLE hardening_changes; DROP TABLE hardening_results; DROP TABLE hardening_runs;
DROP TABLE edge_state; DROP TABLE edge_snippets; DROP TABLE domains;
