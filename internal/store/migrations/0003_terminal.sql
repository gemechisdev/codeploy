-- +goose Up
-- Every browser shell records a row (plan P3: "every session has an audit row"). actor fields are
-- denormalized like audit_events so history survives a user being removed.
CREATE TABLE terminal_sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  user_email TEXT NOT NULL,
  ip TEXT NOT NULL,
  target TEXT NOT NULL CHECK (target IN ('host','container')),
  target_name TEXT NOT NULL,
  as_user TEXT NOT NULL,
  is_root INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  end_reason TEXT NOT NULL DEFAULT '',
  recorded INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX terminal_sessions_recent ON terminal_sessions(started_at DESC, id DESC);
-- Concurrency limits count open rows per user; the index keeps that check cheap on a busy panel.
CREATE INDEX terminal_sessions_open ON terminal_sessions(user_id, ended_at);

-- Defaults chosen here: 3 concurrent shells per user is generous for one browser and bounds a fork bomb;
-- recording is off because transcripts contain whatever the operator types, including secrets.
INSERT OR IGNORE INTO settings(key, value) VALUES ('terminal.max_sessions_per_user','3');
INSERT OR IGNORE INTO settings(key, value) VALUES ('terminal.record_sessions','false');

-- +goose Down
DROP TABLE terminal_sessions;
