-- +goose Up
-- Phase 5: git sources, deploy keys, webhooks and deploy hooks. Additive only: 0001-0004 are frozen.
-- resource_sources and resource_credentials already exist (0004); this file adds the rest of the git surface.

-- Panel-level git connections (plan P5: "Git connections"). kind 'github_app' is reserved for P7 and cannot be
-- created by this phase's API; the CHECK keeps a stray insert from claiming a credential path that does not exist.
CREATE TABLE git_connections (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('deploy_key','github_app')),
  name TEXT NOT NULL,
  created_at TEXT NOT NULL
);
-- No index here on purpose: a panel holds a handful of connections and the list is returned whole.

-- The public half of each resource's deploy key, which is what the operator pastes into the repository settings.
-- The private half goes in resource_credentials (0004) under kind 'deploy_key', sealed with the master key; this
-- table cannot produce it, and no route returns it.
CREATE TABLE deploy_keys (
  id TEXT PRIMARY KEY,
  resource_id TEXT NOT NULL UNIQUE REFERENCES resources(id) ON DELETE CASCADE,
  title TEXT NOT NULL,
  public_key TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  created_at TEXT NOT NULL
);

-- One deploy hook per resource. The plaintext token is returned exactly once, at creation or rotation; only its
-- SHA-256 goes in here, so a database dump cannot deploy anything. Lookup is by hash, which is why the column is
-- UNIQUE rather than indexed alongside a scan.
CREATE TABLE deploy_hooks (
  id TEXT PRIMARY KEY,
  resource_id TEXT NOT NULL UNIQUE REFERENCES resources(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL,
  image_policy TEXT NOT NULL,
  created_at TEXT NOT NULL,
  last_used_at TEXT
);

-- The HMAC secret for a resource's GitHub webhook, sealed with the master key like every other credential here.
-- It cannot be hashed (the receiver must recompute the signature), so it is encrypted, and the API only ever
-- shows the prefix.
CREATE TABLE webhook_secrets (
  resource_id TEXT PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
  secret_enc BLOB NOT NULL,
  secret_prefix TEXT NOT NULL,
  created_at TEXT NOT NULL
);

-- Every delivery the panel received, accepted or not: the operator's audit trail against a repository, and the
-- replay guard. (provider, delivery_id) is unique so a retried delivery loses its race with the original insert
-- instead of passing a check-then-act window; the rejection row that follows records a distinct delivery_id, so
-- the retry itself stays visible in the list.
CREATE TABLE webhook_deliveries (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL DEFAULT 'github',
  delivery_id TEXT NOT NULL,
  received_at TEXT NOT NULL,
  event TEXT NOT NULL,
  result TEXT NOT NULL CHECK (result IN ('success','ignored','rejected')),
  reason TEXT NOT NULL DEFAULT '',
  commit_sha TEXT,
  resource_id TEXT REFERENCES resources(id) ON DELETE SET NULL,
  UNIQUE (provider, delivery_id)
);
-- The list is newest-first by ULID, exactly like deployments: id is time-ordered, so it doubles as the cursor.
CREATE INDEX webhook_deliveries_recent ON webhook_deliveries(id DESC);
CREATE INDEX webhook_deliveries_resource ON webhook_deliveries(resource_id, id DESC);

-- Clone runs under these: a wall-clock timeout and a cap on the working tree, both enforced by the git module.
INSERT OR IGNORE INTO settings(key, value) VALUES ('git.clone_timeout_s','300');
INSERT OR IGNORE INTO settings(key, value) VALUES ('git.max_clone_mb','2048');

-- +goose Down
DROP TABLE webhook_deliveries;
DROP TABLE webhook_secrets;
DROP TABLE deploy_hooks;
DROP TABLE deploy_keys;
DROP TABLE git_connections;
