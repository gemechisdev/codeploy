# ADR 0005: Compile-time modules with a database enable flag
Status: accepted
Decision: modules implement the small interface in `internal/modules` (key, migrations, permissions, routes, jobs, start/stop). The `modules` table decides what runs. A disabled module registers no routes and no jobs.
Why: keeps the core small and defers a plugin framework to P7.
Consequences: until a module is implemented its routes exist as stubs that return 404 `module_disabled` (module off) or 501 (on but not built) and still declare their permission for the authz matrix.
