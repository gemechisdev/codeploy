-- +goose Up
-- Phase 4: projects, environments, resources and the deployment engine's durable state.
-- Additive only: 0001-0003 are frozen. resources.active_deployment_id is the ONLY pointer the edge routes from
-- (plan 3.8): a deployment that never reaches the switch step can never move traffic.

CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

CREATE TABLE environments (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (project_id, name)
);

-- slug is globally unique: container, image and volume names embed it (plan 3.8 idempotency).
-- active_deployment_id has no FOREIGN KEY on purpose: resources and deployments reference each other, and the
-- switch is a single UPDATE inside the engine's transaction.
CREATE TABLE resources (
  id TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  type TEXT NOT NULL CHECK (type IN ('app','compose','static','database','service')),
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  config_json TEXT NOT NULL,
  desired_state TEXT NOT NULL DEFAULT 'stopped' CHECK (desired_state IN ('running','stopped')),
  active_deployment_id TEXT,
  started_at TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX resources_env ON resources(environment_id);

-- Values are sealed with the master key (internal/crypto). Secrets are write-only: no query in the code base
-- selects value_enc for an is_secret row into an API response, and no permission for revealing one exists.
CREATE TABLE resource_env_vars (
  id TEXT PRIMARY KEY,
  resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value_enc BLOB NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('build','runtime','both')),
  is_secret INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  UNIQUE (resource_id, key)
);

CREATE TABLE volumes (
  id TEXT PRIMARY KEY,
  resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  mount_path TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'bind',
  created_at TEXT NOT NULL,
  UNIQUE (resource_id, name),
  UNIQUE (resource_id, mount_path)
);

CREATE TABLE resource_sources (
  resource_id TEXT PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('public_git','deploy_key_git','registry')),
  repo_url TEXT,
  branch TEXT,
  build_path TEXT,
  dockerfile TEXT,
  path_filters_json TEXT NOT NULL DEFAULT '[]',
  deploy_key_id TEXT
);

-- Per-resource credentials, sealed: kind 'deploy_key' (private key PEM) and 'registry' ({server,username,password}).
-- Phase 5 owns the endpoints that write them; the engine only reads them, into 0600/0700 temp files that are
-- removed after use.
CREATE TABLE resource_credentials (
  resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('deploy_key','registry','git_token')),
  blob_enc BLOB NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (resource_id, kind)
);

CREATE TABLE deployments (
  id TEXT PRIMARY KEY,
  resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  status TEXT NOT NULL CHECK (status IN ('queued','preparing','building','starting','verifying','switching','observing','succeeded','failed','cancelled','interrupted','rolled_back')),
  step TEXT NOT NULL DEFAULT 'queued',
  trigger TEXT NOT NULL CHECK (trigger IN ('manual','webhook','deploy_hook','rollback')),
  mode TEXT NOT NULL DEFAULT 'pull' CHECK (mode IN ('pull','build')),
  branch TEXT,
  commit_sha TEXT,
  commit_message TEXT,
  image_ref TEXT NOT NULL DEFAULT '',
  image_digest TEXT,
  container_id TEXT,
  upstream TEXT NOT NULL DEFAULT '',
  previous_deployment_id TEXT,
  retained INTEGER NOT NULL DEFAULT 0,
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  heartbeat_at TEXT,
  observe_until TEXT,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  rollback_of TEXT,
  rollback_reason TEXT,
  error TEXT,
  health_done INTEGER NOT NULL DEFAULT 0,
  health_max INTEGER NOT NULL DEFAULT 0,
  health_last TEXT,
  preflight_json TEXT
);
CREATE INDEX deployments_resource ON deployments(resource_id, started_at DESC, id DESC);
CREATE INDEX deployments_status ON deployments(status);

CREATE TABLE deployment_logs (
  deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  at TEXT NOT NULL,
  stream TEXT NOT NULL CHECK (stream IN ('stdout','stderr','system')),
  line TEXT NOT NULL,
  PRIMARY KEY (deployment_id, seq)
);

INSERT OR IGNORE INTO settings(key, value) VALUES
 ('deploy.build_timeout_s','1800'),('deploy.release_timeout_s','600'),('deploy.max_log_lines','20000'),
 ('deploy.stop_old_first','false');

-- +goose Down
DROP TABLE deployment_logs;
DROP TABLE deployments;
DROP TABLE resource_credentials;
DROP TABLE resource_sources;
DROP TABLE volumes;
DROP TABLE resource_env_vars;
DROP TABLE resources;
DROP TABLE environments;
DROP TABLE projects;
