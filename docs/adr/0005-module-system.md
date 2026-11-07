# ADR 0005: Compile-time modules with a database enable flag
Status: accepted as a target shape; the interface is aspirational (see T33), not the system the code runs today.
Decision: modules implement the small interface in `internal/modules` (key, migrations, permissions, routes, jobs, start/stop). The `modules` table decides what runs. A disabled module registers no routes and no jobs.
Why: keeps the core small and defers a plugin framework to P7.
Consequences: until a module is implemented its routes exist as stubs that return 404 `module_disabled` (module off) or 501 (on but not built) and still declare their permission for the authz matrix.

Current state (DECISIONS T33, 2026-10-22): the `Module` interface and `Registry` in `internal/modules/module.go`
are not used by the daemon — `Registry.Register` is never called, and routes are wired by `internal/api` route
tables instead. The types are kept for future adoption rather than deleted mid-build. This ADR describes the target
shape; a follow-up can either adopt `Register` or rewrite this ADR when P8 starts. Do not read it as a description
of running code.
