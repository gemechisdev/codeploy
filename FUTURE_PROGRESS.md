# FUTURE PROGRESS — where codeplo stands and what "finished" means

Snapshot: 2026-10-21 (post T1/T2). Companion files: `PROGRESS.md` (what is built, per phase, with the
evidence tables), `docs/DECISIONS.md` (why each judgment call went the way it did), `FUTURE_TASKS.md` (the exhaustive
remaining checklist), `docs/HANDOFF_NOTES.md` (observations, host quirks, what to avoid), `PROMPT.md` (the brief for
the next agent).

`PROGRESS.md` is a record of the past. This file is the map for the rest of the build, written by the agent that
built P0–P7's backend under a "code only, compile-checked, no tests" instruction.

## 1. State of the build, honestly

| Phase | Code | Proof |
|---|---|---|
| P0 Foundations | complete | threat model + 6 ADRs merged; Docker spike **run and green**; Caddy zero-drop spike **flaky/unmet** |
| P1 Control plane | complete | installer verified on a real systemd host; auth/2FA/audit/authz verified; **idle RSS budget unmet** (78.8 MB vs 50 MB); **CI never executed on a runner** |
| P2 Edge + hardening | complete (P2a/P2b/P2c code) | P2a verified on WSL (real Caddy, 15/15 installer assertions); Tier-1 apply verified; **P2b/P2c VM fault-injection matrix never run**, so firewall/SSH apply stays flag-gated plan-only |
| P3 Terminal + logs | complete | race-tested (13 packages ok); container half runtime-verified against real Docker 29.1.3; **host-shell run (spike E) never executed**; frontend live-mode pass never done |
| P4 Projects + deploys | complete | **compiles only — zero tests executed**; §3.8 fault-injection matrix owed |
| P5 Git + CI/CD | complete | **compiles only**; the HMAC/replay/skip-deploy/oversize-clone proofs are unrun; reusable Actions workflow never executed |
| P6 Operations | backend complete | **compiles only**; migration 0006 applied successfully via `store.Migrate` (T1); frontend exists mock-driven, never run live |
| P7 Extensibility | templates (J6) + GitHub App (G5) + commit status (G7) + PR previews (G8) complete; **J4 extensions and J7 MCP not started** | **compiles only**; migration 0007 applied successfully via `store.Migrate` (T1); `codeplo templates check` and `codeplo github-app check` were run by hand against scratch inputs and behaved as designed |
| P8 Scale/advanced access | not started (explicitly later) | — |
| P9 Release hardening | not started | no signed artefacts, no `codeplo update`, arm64 never built |

The one sentence that matters: **every phase from P4 onward was built under an instruction not to run tests, so
P4–P7's backend is unproven.** Compilation is not evidence. The next agent's first job is turning that into evidence.

## 2. The contract is the reason P7's remaining items are a decision, not a task

`api/openapi.yaml` is contract authority and `test/authz/routes_test.go` asserts the Go route set equals it exactly —
no extra route, none missing. All 110 contract operations are implemented; **zero routes are served by the 501 stub**.
The contract contains **no route surface at all** for extensions, an extension API, outbound event webhooks, or MCP.
That is why P7's templates, GitHub App, commit status and previews were built behind the CLI, the daemon's loops and
outbound calls instead of as invented routes.

So J4 (extensions: registration, scoped tokens, event delivery with retries, action endpoints) and J7 (MCP server
module) each start with a question the last agent cannot answer for you: **add the routes to the contract, or deliver
the capability without them.** Adding them is allowed (it is the plan's own P7 scope: "MCP client can list resources
and trigger a deploy", "a sample out-of-process extension receives events and invokes an action with a token") but it
re-runs the stub generator, the authz matrix and the mock parity tests, so it must be one deliberate commit, not a
side effect. Decide, write the entry in `docs/DECISIONS.md`, then build.

## 3. Milestones, in the only order that works

**M1 — Make the database real (half a day).** Apply migrations 0001–0007 to a scratch database with a real
`goose up`, boot the daemon against it, and let every later phase's tests run. A bad migration blocks daemon boot,
which blocks every runtime check in the project, so nothing else is worth doing first.

**M2 — Turn P4–P7 into evidence (the bulk of the remaining work).** Engine fault injection over every §3.8 row, the
P5 security paths, the P6 operations behaviour, the P7 preview lifecycle, plus contract-conformance runs of the real
handlers against what `web/src/mock` asserts. Expect this to find bugs; that is the point. Standing rule: never
weaken a test to make it pass — the test is the specification.

**M3 — Run the things that only a machine can run.** Spike E (host PTY) on real systemd, the P2b/P2c VM matrix, CI on
a runner (the only place the RSS budget and `make gen` idempotence mean anything), installer fixtures serving all
seven artefacts.

**M4 — Finish P7's code.** J4 extensions, then J7 MCP, then the frontend's live-mode passes for
templates/git/monitoring/backups/previews.

**M5 — P9.** GoReleaser, cosign, `codeplo update` + `rollback`, arm64, docs. P8 stays later.

## 4. What is likely to break, ranked

1. **Migrations 0006/0007 unapplied** — hand-written SQL that has never met a database. First `goose up` will find
   something.
2. **Zero executed tests for P4–P7** — the deploy engine, git module, previews, templates, monitor, notify,
   databases, cron and objectstore have no test files at all today.
3. **Live mode has never run** — the SPA has only ever been exercised against the mock; the WS/SSE paths, the
   `check:mock-free` guard and the step-up flow are unverified against a real daemon.
4. **`/terminal/ticket` step-up says three different things** — OpenAPI declares `x-step-up: true` on the route, the
   server requires step-up only for `as_root`, the mock follows the server. Contract, server and UI must say one
   thing; decide it deliberately.
5. **`POST /setup/operator` with `mode:"create"` returns 501** — a real gap in a first-run path, not a stub of a
   later phase.
6. **Idle RSS 78.8 MB vs the 50 MB budget** — reported, not tuned. Profile B/C have never been measured.

## 5. Standing instructions that do not expire

- **No credentials in this repo, ever.** The WSL VM's root password is supplied by the operator per session. History
  is not rewritten. Secrets stay write-only through the API; build-time secrets are BuildKit secret mounts, never
  `ARG`; dump credentials go in the exec environment, never argv; git tokens never appear in URLs or argv; registry
  credentials live in an ephemeral 0700 `DOCKER_CONFIG`.
- Smallest fix. Never delete or weaken a test to make it pass. Never report a skipped or unrun check as a pass.
- A defect you can name gets fixed, not parked.
- Judgment calls go in `docs/DECISIONS.md`; `PROGRESS.md` is updated before every commit; one commit per unit of work;
  push when the local gates pass so CI runs the slow ones.
- A running service is shared state. Nothing destructive or write-bearing against a live or shared system without
  explicit per-action approval; no force-push, no history rewrite, no bare `git stash` / `git reset --hard`.
- Verify the state, not the artifact — a compile says the code type-checks, nothing more.
