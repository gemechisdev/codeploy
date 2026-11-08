-- +goose Up
-- Static sites and Compose stacks are deployable; turn the gates on for existing panels.
UPDATE feature_flags SET enabled=1 WHERE key IN ('deploy.static','deploy.compose');

-- +goose Down
UPDATE feature_flags SET enabled=0 WHERE key IN ('deploy.static','deploy.compose');
