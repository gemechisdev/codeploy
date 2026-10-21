-- +goose Up
-- Phase 6: operations. Additive only: 0001-0005 are frozen.

-- The metrics ring buffer (plan: "SQLite ring buffer"). Rows are inserted by the sampler and pruned back to
-- metrics.max_samples, oldest first, so the table cannot grow without bound on a small host. id is a ULID, which
-- makes it the ordering key, the prune cursor and the range filter all at once; `at` is kept for display.
-- The columns are the contract's HostMetrics, so a chart is a SELECT and never a mixture of history and present
-- values. The five host-only fields are 0 on a resource point: a container has no disk total and no daemon.
CREATE TABLE metrics_samples (
  id TEXT PRIMARY KEY,
  at TEXT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('host','resource')),
  ref_id TEXT NOT NULL DEFAULT '',
  cpu_percent REAL NOT NULL DEFAULT 0,
  memory_used_mb INTEGER NOT NULL DEFAULT 0,
  memory_total_mb INTEGER NOT NULL DEFAULT 0,
  daemon_rss_mb REAL NOT NULL DEFAULT 0,
  disk_used_gb REAL NOT NULL DEFAULT 0,
  disk_total_gb REAL NOT NULL DEFAULT 0,
  net_rx_kbps REAL NOT NULL DEFAULT 0,
  net_tx_kbps REAL NOT NULL DEFAULT 0,
  container_count INTEGER NOT NULL DEFAULT 0
);
-- The read is always "the newest N points of one scope", newest-first, which is exactly this index.
CREATE INDEX metrics_samples_window ON metrics_samples(scope, ref_id, id DESC);

-- Notification channels. The config (bot token, webhook URL, SMTP password) is sealed with the master key exactly
-- like resource_credentials: it is written once, opened only by the sender, and no route reads it back. What the
-- API shows instead is destination_hint, a non-secret label ("chat …4821") computed from the config at creation.
CREATE TABLE notification_channels (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('telegram','discord','email','webhook')),
  name TEXT NOT NULL,
  destination_hint TEXT NOT NULL,
  config_enc BLOB NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL
);

-- Alert rules. `key` is unique because the catalog defines what each rule means: one rule per condition, exactly as
-- the mock enforces ("A rule for disk_low already exists"). The condition expression and its title live in code
-- (internal/modules/monitor), never in the row, so a database dump cannot invent a condition the evaluator does not
-- understand. threshold is the operator's number as text: the same column holds '15', '90' and 'running'.
CREATE TABLE alert_rules (
  id TEXT PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  threshold TEXT NOT NULL,
  channel_ids_json TEXT NOT NULL DEFAULT '[]',
  cooldown_s INTEGER NOT NULL DEFAULT 900,
  enabled INTEGER NOT NULL DEFAULT 0,
  last_triggered_at TEXT,
  created_at TEXT NOT NULL
);

-- Dedupe state: one row per (rule, subject). container_down on two different resources is two incidents; the same
-- resource flapping for an hour is one. Cooldown is measured from last_notified_at, so a rule that keeps firing
-- escalates on the rule's own schedule instead of per notification attempt.
CREATE TABLE alert_firings (
  rule_id TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
  subject TEXT NOT NULL,
  first_fired_at TEXT NOT NULL,
  last_notified_at TEXT NOT NULL,
  occurrences INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (rule_id, subject)
);

-- Backup schedules. destination is the enum the contract exposes; an s3 schedule points at the destination the
-- daemon was started with (CODEPLO_S3_* environment), because a bucket's secret key has no place in a plaintext
-- settings table and no API may accept it after creation. The resource name the panel shows is joined from
-- resources at read time, never copied here, so renaming a resource cannot strand a stale label on a schedule.
CREATE TABLE backup_schedules (
  id TEXT PRIMARY KEY,
  resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  cron TEXT NOT NULL,
  destination TEXT NOT NULL CHECK (destination IN ('local','s3')),
  retention_days INTEGER NOT NULL,
  last_run_at TEXT,
  last_run_status TEXT CHECK (last_run_status IN ('success','failed','running')),
  created_at TEXT NOT NULL
);
CREATE INDEX backup_schedules_resource ON backup_schedules(resource_id);

-- Every attempt, kept until the schedule's retention window passes. A failed run is a row with an error and no
-- checksum: the operator's first question after a missed backup is whether it failed or never ran.
CREATE TABLE backup_runs (
  id TEXT PRIMARY KEY,
  schedule_id TEXT NOT NULL REFERENCES backup_schedules(id) ON DELETE CASCADE,
  status TEXT NOT NULL CHECK (status IN ('running','success','failed')),
  size_bytes INTEGER NOT NULL DEFAULT 0,
  checksum_sha256 TEXT,
  location TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  error TEXT,
  -- Why the run happened. Only 'schedule' and 'manual' are reachable through the API; pre_restore and pre_deploy
  -- are written by the engine and the restore wizard, and they still have to survive their retention window.
  trigger TEXT NOT NULL DEFAULT 'schedule' CHECK (trigger IN ('schedule','manual','pre_restore','pre_deploy'))
);
CREATE INDEX backup_runs_schedule ON backup_runs(schedule_id, started_at DESC, id DESC);
CREATE INDEX backup_runs_recent ON backup_runs(id DESC);

-- Cron jobs executed inside a resource (K2). The schedule is a 5-field expression evaluated by internal/jobs at
-- minute resolution; command_json is the argv array, so a job is an argument list and never a shell string.
CREATE TABLE resource_cron_jobs (
  id TEXT PRIMARY KEY,
  resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  cron TEXT NOT NULL,
  command_json TEXT NOT NULL,
  timeout_s INTEGER NOT NULL DEFAULT 600,
  enabled INTEGER NOT NULL DEFAULT 1,
  last_run_at TEXT,
  last_status TEXT CHECK (last_status IN ('success','failed','skipped')),
  created_at TEXT NOT NULL
);
CREATE INDEX resource_cron_jobs_resource ON resource_cron_jobs(resource_id);

-- Execution history (K2's "Execution history"). output is the bounded tail of the job's combined output: long
-- enough to diagnose a failure, short enough that a minute-resolution job cannot fill the disk.
CREATE TABLE cron_job_runs (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL REFERENCES resource_cron_jobs(id) ON DELETE CASCADE,
  at TEXT NOT NULL,
  exit_code INTEGER NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL CHECK (status IN ('success','failed','skipped')),
  output TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX cron_job_runs_job ON cron_job_runs(job_id, id DESC);

-- Alert presets: created disabled (plan §8.1), so nothing notifies until the operator picks a channel. disk_low is
-- the one exception the wizard can turn on, and only because the threshold and the channel are both chosen there.
INSERT OR IGNORE INTO alert_rules(id, key, threshold, cooldown_s, enabled, created_at) VALUES
 ('alr_disk_low', 'disk_low', '15', 900, 0, '1970-01-01T00:00:00.000Z'),
 ('alr_container_down', 'container_down', 'running', 900, 0, '1970-01-01T00:00:00.000Z'),
 ('alr_deploy_failed', 'deploy_failed', 'failed', 900, 0, '1970-01-01T00:00:00.000Z'),
 ('alr_cert_expiring', 'cert_expiring', '14', 900, 0, '1970-01-01T00:00:00.000Z');

INSERT OR IGNORE INTO settings(key, value) VALUES
 ('metrics.sample_interval_s','60'),('metrics.max_samples','20160'),
 ('alerts.eval_interval_s','60'),('notify.timeout_s','10'),
 ('logs.retention_days','14'),('logs.max_rows','200000'),('cron.history_per_job','50'),
 ('backup.timeout_s','900');

-- +goose Down
DROP TABLE cron_job_runs;
DROP TABLE resource_cron_jobs;
DROP TABLE backup_runs;
DROP TABLE backup_schedules;
DROP TABLE alert_firings;
DROP TABLE alert_rules;
DROP TABLE notification_channels;
DROP TABLE metrics_samples;
DELETE FROM settings WHERE key IN ('metrics.sample_interval_s','metrics.max_samples','alerts.eval_interval_s',
 'notify.timeout_s','logs.retention_days','logs.max_rows','cron.history_per_job','backup.timeout_s');
