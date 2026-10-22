-- +goose Up
-- Phase 7 extensions (J4). No HTTP surface in the contract: registration and delivery are daemon-local (T53).
-- Tokens are hashed at rest (same pattern as deploy hooks). No separate enabled column on delivery rows —
-- state is attempt/status only (0006 lesson).

CREATE TABLE extensions (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  endpoint_url TEXT NOT NULL,
  secret_enc BLOB NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE extension_tokens (
  id TEXT PRIMARY KEY,
  extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  hash TEXT NOT NULL UNIQUE,
  scopes TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_used_at TEXT
);

CREATE TABLE extension_subscriptions (
  extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
  event TEXT NOT NULL,
  PRIMARY KEY (extension_id, event)
);

CREATE TABLE extension_deliveries (
  id TEXT PRIMARY KEY,
  extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
  event TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending','delivered','failed','dead')),
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX extension_deliveries_due ON extension_deliveries(status, next_attempt_at, id);

INSERT OR IGNORE INTO feature_flags(key, description, enabled, release_gated) VALUES
 ('extensions.outbound','Deliver events to registered out-of-process extensions.',0,0),
 ('mcp.server','Expose an MCP server for AI agents (stdio/unix); same authz as the API.',0,0);

-- +goose Down
DELETE FROM feature_flags WHERE key IN ('extensions.outbound','mcp.server');
DROP TABLE extension_deliveries;
DROP TABLE extension_subscriptions;
DROP TABLE extension_tokens;
DROP TABLE extensions;
