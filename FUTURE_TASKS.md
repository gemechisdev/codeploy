# FUTURE TASKS — every remaining unit of work, numbered

Companion to `FUTURE_PROGRESS.md`. This is the complete list, including fixes, as one flat checklist: 74 tasks in 7
groups. Numbering is stable — reference tasks as `T17`, never as "the third one". Group A and group C are what blocks
everything; group E is the remaining code; the rest is proof and polish.

Each task names the files to touch and **how you know it is done**. If a task's verification is "it compiles", the
task is not done.

> **Status as of 2026-11-07.** This list was written when P4–P7 had no tests and P9 had no release pipeline. It is
> kept verbatim as the stable numbering reference (`T17` must always mean the same task), but many items are now
> closed. Read `FUTURE_PROGRESS.md` §1 for the current phase-by-phase proof map and
> `docs/site/verification-matrix.md` for per-row proof status before starting work from this list.
>
> | Group | State now |
> |---|---|
> | A (T1–T5) | **Done** — migrations reach v8 via `store.Migrate`; `make migrate-check`, `TestMigrateSeeds`, `TestQueryPlans`, `TestPreviewNoFK` |
> | B (T6–T26) | **Mostly done.** Handler/audit/secret/guard proof added in `internal/api/handlers_phase_test.go`; T6/T7 engine `Run` fault injection and T25 live mock-vs-handler conformance still owed |
> | C (T27–T43) | **Done** — the full local race tree is green, and the `go-arm64` CI job is green; T34/T28/T29/T30/T31/T32 all recorded in `docs/DECISIONS.md` |
> | D (T44–T52) | **Hardware matrix NOT RUN** — see verification matrix. CI itself has run, failed twice, been fixed, and is now green on `go`, `go-arm64` and `web`; the host matrix has not |
> | E (T53–T62) | **Code done.** T53 decided (no new routes), T54–T61 landed, `codeplo mcp check --json` verified. T62's live-daemon pass still NOT RUN |
> | F (T63–T68) | **Code done; first public tag/release publication pending.** GoReleaser + cosign + `update`/`rollback` + one-line installer + README + arm64 CI job + operator guide + a11y/i18n scaffolding exist; `release.yml` has not yet executed on a tag |
> | G (T69–T74) | **Done** — `make test` includes vitest, `.nvmrc`/CI Node pin, gen idempotence gate exercised in CI, `docs/ENV.md` (incl. MCP vars), ADR 0007, T74 boundary. T70 WSL host cleanup remains a host-side decision |
>
> Historical lines below that say CI or `release.yml` has "never executed" describe the moment this checklist was
> written. CI run `42343047804` on `0178c65` is green on all three jobs. The release run is tracked separately in
> `docs/RELEASE_NOTES.md` and `docs/site/verification-matrix.md`.

## Group A — Unblock the database (do this first, nothing else works without it)

- **T1** Apply migrations 0001–0007 to a scratch database with the real `store.Migrate` (goose) path and assert
  `goose_db_version` reaches 7. Files: `internal/store/migrations/*.sql`, `internal/store/db.go`. Done when a fresh
  `codeplod` boot against an empty data dir succeeds. 0006 and 0007 have **never met a database**.
- **T2** Add the missing scratch-DB tooling so T1 is repeatable: a `make migrate-check` (or `scripts/migrate_check.sh`)
  that creates a temp DB, runs the migration set, boots nothing, and exits non-zero on error. There is no such target
  today (`Makefile` has `gen lint test web build run dist`).
- **T3** Verify every hand-written seed in 0005/0006/0007 actually inserts: the four disabled alert presets, the
  `git.preview_*` settings, `git.preview_deploys`, `metrics.*`, `logs.*`, `cron.history_per_job`, `backup.timeout_s`.
  Done when a test asserts each expected `settings.key`, `feature_flags.key` and `alert_rules.key` row exists after a
  migration run.
- **T4** Confirm `resources`, `deployments`, `domains`, `pr_previews`, `webhook_deliveries` indexes match the queries
  the code actually issues (`findSourceByRepo`'s `NOT IN (SELECT preview_resource_id …)`, `oldestPreviews`'s
  correlated count, `SeriesSince`'s ULID bound, `duePreviews`'s `expires_at<=?`). Done with `EXPLAIN QUERY PLAN` in a
  test, not by reading.
- **T5** Decide and record whether `pr_previews`' deliberate lack of `FOREIGN KEY` (documented in
  `docs/DECISIONS.md`) survives contact with `foreign_keys=1` on the DSN — i.e. write the test that deletes a parent
  resource and proves the preview row survives and the sweep reconciles it.

## Group B — Turn P4–P7 from compiling code into tested code

No test file exists for any of these packages today. Each task below is one commit.

- **T6** `internal/modules/deploy` — engine state machine: `observing` → `succeeded`, healthy-then-unhealthy → automatic
  rollback to the previous container, release-command failure, cancel after creation, per-step timeouts, disk-threshold
  refusal, RAM-capped build refusal, hung step. Fake the `runtime.Manager` port.
- **T7** `internal/modules/deploy` — boot recovery: interrupted deployments become `interrupted`, leftover containers
  are removed **before** a worker claims anything, the active container's address is refreshed after a runtime restart,
  orphan sweep never touches a `retained` image.
- **T8** `internal/modules/deploy` — §3.11 isolation: one test per policy row that attempts the violation (privileged,
  host network, `docker.sock`, forbidden bind, extra caps, compose smuggling) and asserts `Approved` cannot be minted
  except by `Enforce`, and that `CreateContainer` re-checks.
- **T9** `internal/modules/deploy` — log redaction and retention: secrets never appear in `deployment_logs`, the
  minted GitHub App token joins the redactor, `PruneLogs` exempts active + retained deployments and reports when the
  budget is unreachable instead of cutting a live log.
- **T10** `internal/modules/deploy` — `RemoveVolumeDir`/`reap`: the two confinement guards (slug grammar, direct child
  of the volumes root) refuse `../` and an empty slug with a sentence, and `RemoveVolumeDirs` is off for the ordinary
  resource delete route.
- **T11** `internal/modules/git` — the security paths: a wrong HMAC is refused, a replayed delivery id loses the
  transaction and answers 409, `[skip deploy]` is honoured, path filters gate a push, a deploy hook cannot deploy a
  sibling repository, `MatchImage` accepts `app:<tag>` and `app:<tag>@sha256:…` and rejects `app-evil:latest`, a clone
  over `git.max_clone_mb` fails with `ErrSourceTooLarge`, `VerifySource` refuses a source the engine could never build.
- **T12** `internal/modules/git` — `providers`: `CanonicalRepo` equivalence (`git@host:o/r.git` == `https://host/o/r`,
  host case-insensitive, path case-sensitive), push payload mapping, `ParsePullRequest` refusing only
  nothing-describing payloads, `DecidePullRequest`'s full ordering (base branch → fork → draft → `[skip deploy]`, and
  `closed`/`converted_to_draft` returning before any of it).
- **T13** `internal/modules/git/providers/githubapp.go` — JWT claims, token cache expiry (two minutes before GitHub's
  own), both PKCS#1 and PKCS#8 parse, an encrypted or non-RSA key is refused with instructions, a non-bare `https` API
  base is refused, 401 and 404 come back as their two named sentences, and the response body is never echoed.
- **T14** `internal/modules/previews` — `Build`: parent refusal reasons, config stripping (release command, published
  ports, retention), memory cap, sealed-blob copy without a master key, refresh in place on the next push, per-service
  cap evicting by expiry, `slugTaken`/`hostnameTaken` refusals leaving the transaction empty.
- **T15** `internal/modules/previews` — `Close`/`Sweep`: the four-statement delete order, `ResourceTags` read before the
  cascade, the volume directory claimed only while no other row still wants the hostname, the three sweep reasons,
  one transaction per preview, and the sweep running **with the flag off**.
- **T16** `internal/modules/templates` — parser refusals (multi-service, `command:`/`entrypoint:`/`depends_on:`,
  placeholder outside `environment:`, shipped credential, empty placeholder), the two routes, catalog drop-in
  replacing a bundled key, and `codeplo templates check`'s non-zero exit.
- **T17** `internal/modules/monitor` — sampler differencing (zero rates on the first sample, release-role containers
  excluded), `SeriesSince` ordering, `Prune` as a time window, `PruneOrphans` at boot, evaluator dedupe per
  `(rule, subject)`, cooldown, stale-sample skip, disabling clearing firings.
- **T18** `internal/modules/notify` — channel CRUD field messages, sealed config never read back, `destination_hint`
  computed at creation, no redirects followed, Discord `allowed_mentions: {parse: []}` and rune-boundary truncation,
  Telegram `{"ok":false}`-with-200 treated as failure, SMTP STARTTLS default and `plain` refusal to send a password
  un-upgraded, `Deliver` counting what arrived.
- **T19** `internal/modules/databases` — `Detect` alias sets (`mirror-postgres` yes, `postgresql-exporter` no), dump
  plans' argv/env split (password in env, never argv), `CopyDemuxed` binary safety, `CopyFileOut` size bound and short
  read, `CopyFileIn` `Perm()` masking, `StartRun` refusing a concurrent attempt, `Execute` returning nil once the row
  has a final status, retention deleting payload before row.
- **T20** `internal/modules/databases` — restore: verified pre-restore snapshot always to the host, restore refusing to
  start without it, recorded size as the read bound, `.gz` sniffed from the name, RDB path ending in
  stop → start → bounded wait for running.
- **T21** `internal/objectstore` — SigV4 canonical requests against a recorded expectation, prefix handling
  (`Prefix()` reducing a stored URI, an outside-prefix key refused), endpoint refused for `@`, XML error body preserved
  to 512 bytes, streaming upload of a pre-computed SHA-256.
- **T22** `internal/jobs/cron.go` — the parser: Vixie OR semantics, `dow` 7 → 0, refused `? L W #` and names, `Next`
  strictly after the instant, a never-matching expression terminating, DST-free minute resolution.
- **T23** `internal/modules/cron` — the runner: argv inside the container and never a shell string, container
  resolution order (active deployment → resource label → never release role), `skipped` when the container is gone, no
  stacking an in-flight attempt, output redacted with the resource's own secrets and bounded to the tail.
- **T24** `internal/api` — handler tests for the P4/P5/P6/P7 route groups (only `api_test`, `edge_test`,
  `hardening_apply_test`, `limiter_test`, `security_test`, `terminal_test` exist today): status codes, `fields` keys,
  message wording and explicit `null`s taken from `web/src/mock`, one test per route group.
- **T25** Contract conformance: run the real handlers and the mock against the same fixtures and assert they agree —
  `docs/contract-test.md` and `scripts/contract-test.sh` exist but have never been used for this.
- **T26** Audit integrity per phase: every write route appends its action, the chain verifies, and `doctor
  --verify-audit` detects a tampered, a deleted and a reordered row (`internal/audit` has a test; the per-route
  coverage does not).

## Group C — Known defects and honesty gaps (fix, do not park)

- **T27** `POST /setup/operator` with `mode:"create"` returns **501** (`internal/api/handlers_auth.go:341`). First-run
  path, contract-declared. Implement or change the contract deliberately — but do not leave a first-run screen 501.
- **T28** `/terminal/ticket` step-up: OpenAPI declares `x-step-up: true`, the server requires it only for `as_root`, the
  mock follows the server. Make contract, server and UI state one thing (`docs/DECISIONS.md` entry required).
- **T29** Terminal session recording (`E4`) is declared by the contract and returns `Recorded:false`
  (`internal/api/handlers_terminal.go:325`). Either implement byte-level recording behind
  `terminal.record_sessions` or remove the field from the contract. Decide; today it is a silent capability lie.
- **T30** Idle RSS: 78.8 MB measured against a 50 MB Profile-A budget. Report it in the release notes; do not silently
  "tune" it. Re-measure on the reference VM in CI, which is the only place the number means anything.
- **T31** Caddy zero-drop spike (Spike A) is flaky and the criterion stays **unmet**. Either stabilise the spike
  (deterministic reload timing) or record the measurement honestly in the release notes. Keep the assertion strict.
- **T32** `internal/api/router.go:285-293` — `stub()` is now unreachable: it is attached only to a generated
  `laterPhase` route whose real handler is missing (`router.go:115`), and all 110 contract operations are implemented,
  so no request can ever receive that 501. Decide deliberately: keep it as the safety net for the next phase's generated
  route (and say so in a comment), or delete it and its `stubs_gen.go` fallback so an unimplemented future route 404s as
  unknown rather than pretending to be a known-but-pending one. Do not leave it both ways.
- **T33** `internal/modules/module.go` — `Registry.Register` is never called and the `Module` interface (Key/Migrations/
  Routes/Start/Stop) is unused while routes are wired by `internal/api` tables. Either adopt the module contract or
  delete it; ADR 0005 currently describes a system the code does not run.
- **T34** `RouteDef.NoCSRF` is declared and never read. Either enforce it or remove the field — an unenforced CSRF
  flag is a false sense of coverage.
- **T35** TOTP replay guard is in-memory: a restart lets a code be reused inside its window. Move the consumed-code set
  to the database (bounded, pruned) or document the window as the mitigation with a written reason.
- **T36** `sqlc.yaml` and the sqlc scaffold exist but nothing uses them. Adopt them for one package's queries or delete
  the scaffold. Half-adopted tooling confuses the next contributor more than its absence.
- **T37** `webhook_deliveries` is append-only with no retention. Hook it into P6's `logs.*`-style retention (or a
  settings key of its own) so a busy panel's delivery history cannot grow forever.
- **T38** Monorepo fan-out per delivery (one push, several resources matched by path filters) is schema-ready and
  unbuilt. Implement in `internal/api/handlers_git.go`'s resolution path, or make the single-target rule explicit in
  the contract description.
- **T39** Installer test fixtures must serve **seven** artefacts (they still assume five; the checksum guard is
  `-eq 7`): `codeplod`, `codeplo`, the two systemd units, `codeplo-caddy.service`, `codeplo-pty.socket`,
  `codeplo-pty@.service`. Fix `deploy/install.sh`'s test path and re-run the 15-assertion suite.
- **T40** `${CODEPLO_URL}` / Coolify's `${SERVICE_FQDN}` are refused at parse time because a template deploy had no
  address. Now that `CODEPLO_PREVIEW_DOMAIN` exists, decide whether the template route gets a panel domain too and
  lift the refusal in one branch of `classify` — or keep it and say why.
- **T41** `PROGRESS.md` claims `web/src/features/git/*` does not exist. It does, as
  `web/src/features/resources/tabs/SourceTab.tsx` plus Settings' `GitTab`. Correct the stale claim, and re-state the
  real gap: it has never run against a live daemon.
- **T42** `docs/DECISIONS.md` and `PROGRESS.md` are phase-ordered but have no index of the open gaps. Add a short
  "open, by design" section that lists T27–T41's decisions so the next agent does not re-litigate them.
- **T43** Run `go test -race -count=1 ./...` with `CC="C:/tools/winlibs/mingw64/bin/gcc.exe"` and fix everything it
  finds. It has not been run since P3's gate, so P4–P7's code has never been race-checked.

## Group D — Runtime and environment verification (only a machine can answer these)

- **T44** Spike E — host PTY through the real privilege boundary on a systemd host: recreate
  `tmp/setup_spike_e.sh` from the header comment of `test/spikes/p3_hostpty_spike_test.go` (it was never committed),
  then run the §6.2 P3 matrix: shell as a real sudo operator, refusal with no operator, root off by default, root
  step-up-gated with and without `/etc/codeplo/allow-root`, close-the-tab ending the PTY with no leftover process,
  idle timeout, session limit, an audit row per ticket and per session, helper socket mode letting the unprivileged
  daemon traverse `/run/codeplo-pty`, the dropped-line notice under a slow reader.
- **T45** P2b/P2c VM fault-injection matrix: kill the daemon after apply and confirm the revert still fires; reboot
  before confirm and confirm the previous state returns; external scan on IPv4 **and** IPv6 showing only SSH/80/443;
  Docker's own rules untouched; `sshd -t` failure refused; cloud-init drop-in ordering; fail2ban self-ban guarded.
  Then flip Tier-2/Tier-3 apply flags from plan-only, which is the plan's own gate.
- **T46** Deploy engine against **real Docker** end to end: image deploy → health gate → HTTPS route → rollback to the
  retained digest, then the same §3.8 fault injections as VM cases (kill mid-deploy, reboot between start and switch,
  cancel mid-build, disk full, OOM).
- **T47** PR preview lifecycle on a real host with real DNS: wildcard record under `CODEPLO_PREVIEW_DOMAIN`, flag on,
  open a PR → preview up and reachable over HTTPS → push again → refresh keeps the URL → close the PR → containers,
  routes, preview row and volume directory all gone; then expire-by-TTL and the parent-deleted paths.
- **T48** Panel HTTPS on a real public domain (P2a's unmet criterion) — the tunnel path still working at the same time.
- **T49** CI on a runner, for real: `ci.yml` (lint, test, authz matrix, OpenAPI/generated drift, build embedding
  `web/dist`), `memory-budget.yml`, `release.yml` have **never executed**. Fix what the first real run reports.
- **T50** Profile B and C measurements on the 1 vCPU/2 GB and 2 vCPU/4 GB reference VMs: total host memory, CPU,
  latency and recovery — recorded, not guessed.
- **T51** `deploy/github-actions/codeplo-deploy.yml` has never run. Exercise it against a test repository: build, push,
  call the deploy hook, honour `[skip deploy]`, never echo the token, per-image concurrency group.
- **T52** GitHub App install flow verified against GitHub: a private repo cloning via installation token only when the
  resource carries no credential of its own, a delivery signed with the App secret verifying for a repository the panel
  never configured, and a commit status appearing under one context on a redeploy.

## Group E — Remaining P7 code

- **T53** Decide the extension/MCP **contract question** first: `api/openapi.yaml` has zero route surface for them and
  `test/authz` enforces exact parity. Write the decision (new routes vs daemon-local capability) in
  `docs/DECISIONS.md`, and if routes are added, run `make gen` and update the authz matrix and mock in the same commit.
- **T54** Migration `0008_extensions.sql`: extension registry rows, scoped tokens (hashed like deploy hooks,
  write-only), subscriptions, and a delivery-attempt table with retry state. No `enabled` column nothing can set (the
  0006 lesson).
- **T55** `internal/modules/extensions/{module,registry,deliver,actions}.go` — registration, per-extension event
  filtering (an extension receives only what it subscribed to), HMAC-signed outbound delivery with bounded retries and
  backoff on the durable job queue, and dead-letter state an operator can see.
- **T56** Extension **actions**: a token can invoke only the actions its scopes allow, enforced through the same
  `internal/authz` check the REST API uses — never a parallel permission model.
- **T57** A sample out-of-process extension (in `test/`, not shipped) that receives an event and invokes an action —
  the P7 exit criterion is a working example, not a package.
- **T58** `internal/modules/mcp/{module,tools}.go` — the MCP server module: tools for listing resources/projects and
  triggering a deploy, each running the **same** permission checks as the API, gated by the `mcp` module flag, and
  unable to reveal a secret any route cannot reveal.
- **T59** Make `mcp` and `extensions` real modules: registered, flag-gated, server-side refusal of a disabled endpoint.
  Today the `modules` table seeds them disabled with no package behind them.
- **T60** Frontend: Extensions page (registration, token, subscriptions, delivery history) and the Templates catalog
  **search** — the plan's own P7 frontend list, neither exists (`web/src/features/extensions` is absent,
  `TemplatesPage.tsx` is 58 lines with no search).
- **T61** Frontend: preview-environment views. Previews are today mock settings keys plus the webhook's PR logic; there
  is no list of what the host is running for pull requests. Keep the density of the existing lists (`feedback-ui`
  rule: never expand a row).
- **T62** Frontend live-mode pass for everything P4–P7 built mock-only: projects/resources/deployments, Source tab,
  monitoring charts, notifications, backups + restore wizard, templates, terminal and logs. Set `VITE_API_MODE=live`
  against a real daemon and re-run `npm run lint`, `build`, `check:mock-free`, `check:external`, `check:size`, `test`.

## Group F — P9 release hardening

- **T63** GoReleaser multi-arch (linux/amd64 + arm64), checksums, `make dist` becoming a release rather than a tarball.
- **T64** cosign signatures and installer/`codeplo update` verification before applying — including the current
  installer's tampered-artefact aborts staying green.
- **T65** `codeplo update` with a pre-migration SQLite snapshot, and `codeplo rollback` restoring snapshot + previous
  binary (migrations are forward-only, so rollback restores rather than reverses).
- **T66** arm64 passing the full suite in CI.
- **T67** Documentation site: operator guide (install, DNS for the panel domain and the preview base domain, GitHub
  App setup, S3 destination, PTY helper, module flags), and the security model page derived from `docs/threat-model.md`.
- **T68** Accessibility pass, mobile layouts, command palette completeness, i18n scaffolding (deferred content, but the
  hooks belong in P9 not later).

## Group G — Housekeeping that keeps the next agent fast

- **T69** Add a `make test-web` (or extend `Makefile:20`) so the frontend suite is part of `make test`; today `make
  test` runs only `go test ./...` and the vitest suite is invisible to the Go-side workflow.
- **T70** Clean up state left on the WSL host by previous sessions (`/usr/bin/caddy` copy, `/root/spikes`,
  `/root/spikeC.log`, `/root/codeplo-fixture`) or document it as owned by the fixtures.
- **T71** Confirm `make gen` idempotence in CI (md5 compare `models.gen.go`, `stubs_gen.go`, `schema.d.ts`) — the check
  exists in `ci.yml` but has never run on a runner.
- **T72** Record the environment variables in one operator-facing list. Today `CODEPLO_*` is spread across
  `internal/config/*.go` and `deploy/codeplod.service`; there is no `.env.example` for the daemon.
- **T73** Keep `docs/adr/` current: ADR 0005 describes the module system the code does not use (see T33) and there is no
  ADR yet for previews (their no-FK row and secret-copy rules are real architecture, not a feature note).
- **T74** Decide where P8 begins (agent mode, OIDC, passkeys, more Git providers) and keep it out of everything above.
  It is explicitly later; do not pre-build for it.
