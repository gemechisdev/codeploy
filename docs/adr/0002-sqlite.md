# ADR 0002: SQLite (WAL) via modernc.org/sqlite
Status: accepted
Decision: SQLite in WAL mode with the pure-Go driver, forward-only goose migrations, immediate write transactions.
Why: zero ops, no CGO, ample for a single host. Alternatives (Postgres) add a process and memory.
Consequences: one writer at a time (busy_timeout 5 s); queries are hand-written `database/sql` in P1, sqlc is scaffolded for later.
