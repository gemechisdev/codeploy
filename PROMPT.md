# PROMPT.md — hand this to the next agent

> **Status note (2026-11-07):** this brief was written when J4 extensions / J7 MCP were missing and P4–P7 had no
> executed tests. Both are no longer true: extensions and MCP landed (T53–T59), a P4–P7 real-router test suite
> exists, and the local + CI gates described below have been run. Read `FUTURE_PROGRESS.md` §1 and
> `docs/site/verification-matrix.md` **before** acting on the brief below; what remains is host/runner verification,
> not the backend implementation work this prompt asks for. The standing rules below (never weaken a test, never
> report an unrun check as a pass) still apply unchanged.

Copy everything below the line into a fresh session in this repository. It is self-contained: it tells the agent what
state the code is in, what to do first, and what it must never do. Task IDs (`T1`–`T74`) refer to `FUTURE_TASKS.md`.

---

You are taking over the build of **codeplo** (`C:\Users\hp\projects\codeploy`, Go module
`github.com/codeplo/codeplo`, React/TS frontend in `web/`). A single-binary self-hosted control plane that deploys
containers on a Docker host, terminates HTTPS through a managed Caddy, hardens the host reversibly, and gives operators
a browser terminal. A client is paying for a production-grade product; they judge it by whether it works, not by how
much of it exists.

## Read these five files before touching anything

1. `PROGRESS.md` — what is built, phase by phase, with the evidence tables.
2. `FUTURE_PROGRESS.md` — the honest state of proof, and the milestone order.
3. `FUTURE_TASKS.md` — the complete remaining checklist, `T1`–`T74`.
4. `docs/HANDOFF_NOTES.md` — what works, what wasted time, what the Docker daemon actually does, which patterns this
   codebase expects you to keep.
5. `docs/DECISIONS.md` — every judgment call already made. Do not re-litigate one without writing a new entry that
   replaces it.

Skim `web/docs/implementation-plan.md` §6.2 (exit criteria), §7 (per-phase build plan), §9 (test strategy) and §3.8,
§3.10, §3.11 when you are in that area. `api/openapi.yaml` is contract authority.

## The one fact that defines your job

**P0–P3 were built and tested. P4–P7 were built under an explicit instruction not to run any test.** Everything from
projects/resources/deployments through Git CI/CD, operations, templates, GitHub App, commit status and PR previews
compiles, was reviewed line by line against the schema and the contract, and has **never executed**. Migrations 0006
and 0007 have never been applied to any database. So your task is not primarily to write new features — it is to turn
unproven code into proven code, then finish the two P7 features that are genuinely missing (extensions J4, MCP J7).

## Work in this order. Do not reorder it.

1. **Group A / T1 first.** Apply migrations 0001–0007 to a scratch database with the real goose path and make a
   repeatable check (T2). A bad migration blocks daemon boot, which blocks every runtime verification in the project.
   Fix whatever it finds.
2. **Group C as you meet it.** Any defect already named in group C (a 501 in a first-run path, the step-up disagreement,
   the dead `stub()`, the unused `Module` interface, the in-memory TOTP replay guard) is a task, not a note. If you
   cannot fix it inside the current commit, say so in your report and keep it as an open task with the reason.
3. **Group B — the test pass for P4–P7**, one commit per package, in this order: `deploy` engine → isolation policy →
   recovery → `git` security paths → `providers` → `previews` → `templates` → `monitor`/`notify`/`databases` → `cron` →
   `objectstore` → API handlers (T24) → contract conformance against the mock (T25).
4. **Group D — what only a machine can answer**: spike E host PTY on systemd (T44), the P2b/P2c VM matrix (T45), real
   Docker deploy/fault-injection (T46), the preview lifecycle with real DNS (T47), CI on a runner (T49). Ask before any
   destructive step against a host; WSL root credentials come from the operator for that session only.
5. **Group E — finish P7's code**: the contract decision for extensions/MCP (T53) before any of T54–T59, then the
   frontend (T60–T62).
6. **Group F — P9 release hardening** (T63–T68). Group G is housekeeping; do it when it is cheaper to do it than to
   explain it. **P8 is not in scope.**

## Non-negotiable rules

- **No credentials in this repository. Ever.** No passwords, tokens, `EnvironmentFile` values, or test-host access in
  any file, commit message or log. Do not rewrite git history.
- **Secrets stay write-only.** Sealed at rest, copied as sealed bytes, redacted from logs, delivered by exec
  *environment* (never argv), held in the environment (never a settings row), and never revealed by any route. Do not
  add a reveal endpoint or a reveal permission.
- **Never weaken, delete or skip a test to make a gate pass.** Never report a check you did not run as a pass, and
  never report a skipped one as a pass either. If something is unverified, write "unverified".
- **Compilation is not evidence.** `gofmt -l internal cmd` empty, `go build ./internal/... ./cmd/...`,
  `go vet ./internal/... ./cmd/...` (vet also compiles test files) and `(cd web && npx tsc --noEmit)` are the minimum
  per commit. `go test -race -count=1 ./...` needs
  `CC="C:/tools/winlibs/mingw64/bin/gcc.exe"` on this host. Frontend gates: `npm run lint`, `build`,
  `check:mock-free`, `check:external`, `check:size`, `npm test` (vitest).
- **Update `PROGRESS.md` before every commit**, add a `docs/DECISIONS.md` entry for every judgment call, one commit per
  unit of work, and push when the local gates pass so CI runs the slow ones. If your work changes what a phase's header
  claims ("implemented, compiling, not tested"), change the header.
- **Do not add an HTTP route casually.** `test/authz/routes_test.go` fails if the Go route set and `api/openapi.yaml`
  disagree. Adding a route means editing the contract, re-running `make gen`, and updating the authz matrix and the mock
  — in the same commit, with a written decision.
- **Smallest fix.** No scaffolding for hypothetical futures, no compatibility shims, no comments explaining what the
  code does. Where you must deviate from the plan, write the reason.
- **UI density is the specification.** Never expand a row or a card while fixing it. Copy is label + value, never
  persuasive.
- **A running service is shared state.** No force-push, no history rewrite, no bare `git stash` / `git reset --hard` on
  unknown state, nothing destructive against a live system without explicit per-action approval. Stop fixture units
  before probing `:80`/`:443`.
- Keep the existing patterns: refusal-before-any-write, write + audit row in one transaction, a nil dependency meaning
  "not available" rather than a fake success, `everyMinute` for clock-aligned loops, flags re-read per pass, and the
  executable mock in `web/src/mock` as the source of truth for user-visible messages.

## Report like this, every time

State what you changed, which gates you ran **with their results**, what is still unproven, and any defect you
discovered but did not fix — named, with a task ID. Never end with "should work"; either it passed, or it is untested.
If you are working in parallel with other agents, do not touch a file outside your declared scope, and remember that
another agent's self-report is not authoritative — verify with `git diff`.

Start with T1.
