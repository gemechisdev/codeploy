# ADR 0004: The database is the source of truth
Status: accepted
Decision: desired state lives in SQLite. Caddy config, containers and host settings are derived by idempotent reconcilers that can be re-run at any time.
Why: crash safety and drift repair without manual steps.
Consequences: no reconciler ships in P1; the job queue is durable and jobs must be idempotent (running jobs are re-queued at start).
