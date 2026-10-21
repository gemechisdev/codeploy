-- +goose Up
-- Phase 7: pull-request preview environments (plan G8). Additive only: 0001-0006 are frozen.
--
-- A preview is an ordinary resources row — the engine, the edge, the logs and the terminal all work on it without
-- knowing what it is for — and this table is the panel's memory of *why* it exists and how long it may. That is why
-- neither resource column carries a FOREIGN KEY, even though every other table here uses one:
--
--   - an FK on parent_resource_id with ON DELETE CASCADE would delete this row when an operator deletes the real
--     service, and the preview container would stay running with nothing left that knows it is disposable;
--   - an FK without CASCADE would make deleting the parent fail on a constraint error, which reads as a bug in the
--     resource delete rather than as a preview that has to go first;
--   - an FK on preview_resource_id is the same trap from the other side: an operator who deletes the preview from
--     the resource page is entitled to, and the row that survives is the one that still has the container to reap.
--
-- So the sweep in internal/modules/previews owns reconciliation, and it reconciles against reality rather than
-- against a pointer: a preview whose parent is gone, whose resource is gone, or whose clock ran out is taken down.

CREATE TABLE pr_previews (
  id TEXT PRIMARY KEY,
  provider TEXT NOT NULL DEFAULT 'github',
  -- The service the pull request would change. Its config, its credentials and its environment are what the preview
  -- was built from, and its deletion is what makes the preview garbage.
  parent_resource_id TEXT NOT NULL,
  -- The disposable resource this preview is: built from the PR's head ref, one hostname, removed by the sweep.
  preview_resource_id TEXT NOT NULL,
  number INTEGER NOT NULL,
  -- The ref GitHub reported as the PR head, cloned rather than the sha it accompanies: a preview shows the branch
  -- as it is now, which is what a reviewer is asking when they open the link.
  head_ref TEXT NOT NULL,
  head_sha TEXT NOT NULL DEFAULT '',
  -- One label under the preview base domain, and UNIQUE because two previews answering one hostname would have the
  -- edge choose which route wins.
  hostname TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  -- The sweep's only input: activity moves it forward, so a pull request someone is still pushing to stays up and
  -- one they abandoned comes down.
  expires_at TEXT NOT NULL,
  UNIQUE (provider, parent_resource_id, number)
);
CREATE INDEX pr_previews_expiry ON pr_previews(expires_at, id);
CREATE INDEX pr_previews_parent ON pr_previews(parent_resource_id, number);

-- Previews run code from a branch that has not been reviewed, on this host, in an isolated container. The flag is
-- off until an operator turns it on, and turning it on does nothing until CODEPLO_PREVIEW_DOMAIN is set — the
-- feature needs a wildcard DNS record pointed at this machine before a hostname under it means anything.
INSERT OR IGNORE INTO feature_flags(key, description, enabled, release_gated) VALUES
 ('git.preview_deploys','Build a preview environment for each open pull request. Runs the branch''s code in an isolated container, never a fork and never its release command, capped by git.preview_max and removed at git.preview_ttl_hours. Needs CODEPLO_PREVIEW_DOMAIN.',0,0);

-- The three limits the plan asks for. They are settings rather than environment because an operator changes them
-- as they learn how the repository behaves, and the API can validate each one.
INSERT OR IGNORE INTO settings(key, value) VALUES ('git.preview_ttl_hours','72');
INSERT OR IGNORE INTO settings(key, value) VALUES ('git.preview_max','5');
INSERT OR IGNORE INTO settings(key, value) VALUES ('git.preview_memory_mb','512');

-- +goose Down
DROP TABLE pr_previews;
DELETE FROM feature_flags WHERE key='git.preview_deploys';
DELETE FROM settings WHERE key IN ('git.preview_ttl_hours','git.preview_max','git.preview_memory_mb');
