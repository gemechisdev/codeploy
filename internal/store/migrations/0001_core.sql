-- +goose Up
CREATE TABLE users (
  id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE COLLATE NOCASE, password_hash TEXT NOT NULL,
  role TEXT NOT NULL, totp_secret_enc BLOB, totp_enabled INTEGER NOT NULL DEFAULT 0,
  disabled INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, last_login_at TEXT);
CREATE TABLE recovery_codes (user_id TEXT NOT NULL, hash TEXT NOT NULL, used_at TEXT, PRIMARY KEY (user_id, hash));
CREATE TABLE roles (key TEXT PRIMARY KEY, name TEXT NOT NULL, builtin INTEGER NOT NULL DEFAULT 1);
CREATE TABLE role_permissions (role TEXT NOT NULL, permission TEXT NOT NULL, PRIMARY KEY (role, permission));
CREATE TABLE sessions (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL, csrf TEXT NOT NULL, created_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL, expires_at TEXT NOT NULL, step_up_until TEXT, bootstrap INTEGER NOT NULL DEFAULT 0,
  pending_challenge TEXT, ip TEXT NOT NULL DEFAULT '');
CREATE INDEX sessions_user ON sessions(user_id);
CREATE TABLE api_tokens (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, prefix TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, scopes TEXT NOT NULL,
  user_id TEXT NOT NULL, last_used_at TEXT, expires_at TEXT, created_at TEXT NOT NULL);
CREATE TABLE audit_events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, at TEXT NOT NULL, actor_type TEXT NOT NULL,
  actor_id TEXT NOT NULL, actor_email TEXT, ip TEXT NOT NULL, action TEXT NOT NULL, target_type TEXT NOT NULL,
  target_id TEXT NOT NULL, meta TEXT NOT NULL, prev_hash TEXT NOT NULL, hash TEXT NOT NULL);
-- Defence in depth: no code path updates or deletes audit rows, and the database refuses too.
-- +goose StatementBegin
CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit_events BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit_events BEGIN SELECT RAISE(ABORT, 'audit_events is append-only'); END;
-- +goose StatementEnd
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE feature_flags (key TEXT PRIMARY KEY, description TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 0, release_gated INTEGER NOT NULL DEFAULT 0);
CREATE TABLE modules (key TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, enabled INTEGER NOT NULL, core INTEGER NOT NULL, version TEXT NOT NULL, phase TEXT NOT NULL, ord INTEGER NOT NULL DEFAULT 0);
CREATE TABLE jobs (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, payload TEXT NOT NULL DEFAULT '{}', state TEXT NOT NULL DEFAULT 'queued',
  run_at TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE INDEX jobs_due ON jobs(state, run_at);
-- Single-row first-run state (plan 3.12). Token stored hashed.
CREATE TABLE setup_state (
  id INTEGER PRIMARY KEY CHECK (id = 1), token_hash TEXT, token_expires_at TEXT, admin_id TEXT,
  step TEXT NOT NULL DEFAULT 'token', totp_secret_enc BLOB, operator TEXT, panel_domain TEXT);
INSERT OR IGNORE INTO setup_state(id, step) VALUES (1, 'token');

INSERT OR IGNORE INTO roles(key, name, builtin) VALUES ('owner','Owner',1),('admin','Admin',1),('deployer','Deployer',1),('viewer','Viewer',1);
INSERT OR IGNORE INTO settings(key, value) VALUES
 ('terminal.allow_root','false'),('terminal.idle_timeout_s','900'),('retain_images','5'),('observation_window_s','60'),
 ('step_up_window_s','300'),('hardening_revert_window_s','300'),('disk_preflight_threshold_gb','2'),('session_ttl_h','12');
INSERT OR IGNORE INTO feature_flags(key, description, enabled, release_gated) VALUES
 ('hardening.apply_tier2','Allow applying the reversible default-deny firewall (Tier 2). Release-gated: enable only after the VM fault-injection matrix passes.',0,1),
 ('hardening.apply_tier3','Allow applying guarded SSH hardening (Tier 3). Release-gated; plan-only by default.',0,1),
 ('deploy.compose','Docker Compose resources (checked against the isolation policy).',1,0),
 ('deploy.static','Static site / SPA (built and served in a container, nginx or auto-detected pack).',1,0),
 ('registry_pull_hook','Registry-pull deploy hook for GitHub Actions.',1,0);
INSERT OR IGNORE INTO modules(key, name, description, enabled, core, version, phase, ord) VALUES
 ('hardening','Host hardening','Audit, safe fixes, reversible firewall, guarded SSH.',1,1,'1.2.0','P2',1),
 ('edge','Edge (Caddy)','Managed Caddy, automatic HTTPS, domains.',1,1,'1.2.0','P2',2),
 ('terminal','Terminal','Host and container shells in the browser.',1,0,'1.2.0','P3',3),
 ('deploy','Deployments','Deployment engine, rollout, rollback.',1,1,'1.2.0','P4',4),
 ('git','Git & CI/CD','Deploy keys, webhooks, registry-pull hook.',1,0,'1.2.0','P5',5),
 ('monitor','Monitoring','Metrics history and alert evaluation.',0,0,'1.2.0','P6',6),
 ('notify','Notifications','Telegram, Discord, email and webhook channels.',0,0,'1.2.0','P6',7),
 ('databases','Databases & backups','One-click databases, scheduled backups, restore.',0,0,'1.2.0','P6',8),
 ('templates','Templates','One-click catalog of compose templates.',0,0,'1.2.0','P7',9),
 ('extensions','Extensions','Out-of-process extension API.',0,0,'1.2.0','P7',10),
 ('mcp','MCP server','MCP server for AI agents using the same permissions as the API.',0,0,'1.2.0','P7',11);
-- role_permissions are seeded idempotently from internal/authz (store.Seed); admin edits are never overwritten.
