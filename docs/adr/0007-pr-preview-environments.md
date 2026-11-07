# ADR 0007: Pull-request preview environments

Status: accepted (implemented). Migrations 0001–0008 now apply cleanly through the real `store.Migrate` (goose)
path — `TestMigrateAllToV8` and `make migrate-check` — and `TestPreviewNoFK` proves the no-FK survival and orphan
query this ADR rests on. Still outstanding: engine-level preview build/teardown unit tests (T14/T15) and the
real-DNS lifecycle (T47). See `docs/site/verification-matrix.md`. Nothing in this ADR reports a measured host
result.

## Context

Plan G8 (Phase 7) asks for a disposable environment per open pull request. A preview is an ordinary `resources`
row: it is built by the deploy engine, routed by the Caddy edge, logged and shell-accessible through the same paths
as any service (`internal/modules/previews/previews.go`). `pr_previews` is only the panel's memory of why a
resource exists and how long it may run (migration `internal/store/migrations/0007_previews.sql`). Previews arrive
through the GitHub webhook receiver, the daemon's minute sweep and the CLI; the package adds no HTTP route
(`test/authz` enforces exact OpenAPI parity, T53/DECISIONS).

## Decision

### `pr_previews` carries no FOREIGN KEY

`parent_resource_id` and `preview_resource_id` are plain `TEXT NOT NULL` with no `REFERENCES`, while every other
table in 0004/0007 points at `resources`. Three traps are avoided (0007 comment, DECISIONS "Phase 7"):

- an FK on the parent with `ON DELETE CASCADE` would delete the bookkeeping row when an operator deletes the real
  service, leaving the preview container running with nothing that knows it is disposable;
- an FK without CASCADE (RESTRICT) would make deleting the parent fail on a constraint error, which reads as a bug
  in the resource delete rather than as a preview that has to go first;
- an FK on `preview_resource_id` is the same trap from the other side: an operator who deletes the preview from its
  own resource page is entitled to, and the surviving row is the one that still has the container to reap.

The DSN runs with `_pragma=foreign_keys(1)` per connection (HANDOFF_NOTES §2). Because no FK is declared on these
two columns, the pragma has nothing to enforce on them: deleting a parent or a preview resource cannot cascade into
or be blocked by `pr_previews`. The row survives by construction, not by disabling the pragma. `TestPreviewNoFK`
(T5) now asserts the pragma is on, deletes both a parent and a preview resource, and proves the row survives and
the sweep's orphan query sees it.

Reconciliation belongs to `Sweep` (`internal/modules/previews/sweep.go`), the only owner. It reconciles against
reality, not a pointer: `orphans()` finds previews whose parent or own `resources` row is gone, `duePreviews()`
finds clocks past `expires_at`, and `reason()` checks parent existence, resource existence, the
`git.preview_deploys` flag and the clock, in that order. Each preview is closed in its own transaction and the pass
runs with the flag off, because "off" is an operator asking for none of this to be running.

### Secrets are copied as sealed blobs, without a master key

`resource_env_vars.value_enc` and `resource_credentials.blob_enc` (0004) are sealed with the `internal/crypto`
master key and are write-only: no query reads a secret back into an API response and no reveal permission exists.
`Service.Deps` deliberately holds no master key (`previews.go:27-28`). `copyEnv` and `copyCredentials`
(`internal/modules/previews/build.go`) `SELECT` the `value_enc` / `blob_enc` bytes and re-`INSERT` the same
`[]byte` under the preview resource id; they never call `crypto.Open` or `crypto.Seal`, so they cannot read or
re-seal anything. The preview is the same daemon with the same key, so the copied bytes are valid in their new row.
The gate's refusal of forks (DECISIONS "Phase 7") is load-bearing: a preview holds the parent's secrets in the only
form the panel keeps them, so unreviewed third-party code must never run with them. `previewConfig` also strips the
release command, drops published ports, sets `RetainImages=1` and clamps memory to `git.preview_memory_mb`.

### Cap and eviction are per service

The limits are settings seeded in 0007 (`git.preview_ttl_hours=72`, `git.preview_max=5`,
`git.preview_memory_mb=512`) with `git.preview_deploys` as an off-by-default feature flag; `CODEPLO_PREVIEW_DOMAIN`
and a wildcard DNS record are prerequisites, not settings. In `Build` (`build.go`), `max := s.limit(...)`,
`countPreviews(... parentID)` and `oldestPreviews(..., n+1-max)` make exactly one room for the incoming PR. The cap
is keyed on `parent_resource_id` because the unbounded case is one busy repository opening PRs. `oldestPreviews`
orders by a correlated resource-exists count (resource-gone rows first, a bookkeeping fix), then `expires_at`,
then `id`; eviction calls `s.close()` on each rather than refusing the new build. Refusals are reached before any
write, so a 200-and-recorded delivery never commits a half-built resource; a unique-index race (slug/hostname/
`(provider,parent,number)`) returns an error that unwinds the whole transaction.

### Teardown is four ordered statements

`close()` (`internal/modules/previews/teardown.go`) runs, in one transaction, the same four statements the resource
delete route runs:

1. `DELETE FROM domains WHERE resource_id=?` — the edge route cannot outlive the workload;
2. `DELETE FROM resources WHERE id=?` — cascades the deployment, source, env-var, credential and volume rows;
3. `DELETE FROM pr_previews WHERE id=?`;
4. `EnqueueReap(...)` — the durable reap job that removes containers, image tags and volume directories off the host.

`eng.ResourceTags(...)` is read before step 2 because the image tags live on the deployment rows that cascade away
with the resource. `stillOwnsHostname` guards `RemoveVolumeDirs`: a reopened PR would be given the same derived
slug/directory, so the directory is reaped only while no other `pr_previews` row still claims that hostname.

## Consequences

- There is no database-level referential integrity for previews; correctness rests on `Sweep` plus the four-
  statement `close()`. A stopped sweep leaves garbage rows and containers by design.
- A preview needs all three gates — feature flag, valid `CODEPLO_PREVIEW_DOMAIN`, and a live engine/queue — before
  `Enabled` is true; hostnames are one DNS label under the base domain because the wildcard record matches one label.
- Verification status: T5 is **proven** by `TestPreviewNoFK`; migrations 0006/0007 (and 0008) are **proven to
  apply** by `TestMigrateAllToV8`. `internal/modules/previews/previews_test.go` currently covers only
  `TestCheckWithoutDomain` and `TestHostnameGrammar`, so T14/T15 (build/teardown/Sweep unit tests) remain owed, as
  does T47 (real-DNS lifecycle). No host runtime behaviour or measurement is claimed here.
