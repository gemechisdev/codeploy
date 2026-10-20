# ADR 0003: Caddy managed through its admin API on a unix socket
Status: accepted
Decision: run Caddy as a separate process with the admin endpoint on a unix socket; codeplo pushes JSON config by `@id` route addition and removal.
Why: automatic HTTPS, atomic config loads, no file templating races, no listener on a TCP admin port.
Consequences: the reconciler (P2) is idempotent and derives routes from the database. Spike A (`test/spikes/caddy_spike_test.go`) tests that add and remove drop no connections.

## Spike A result (Caddy v2.8.4, authoring sandbox)
Adding and removing a route through the admin API works, and the unix-socket admin endpoint needs `admin.origins` to include the Host header used (default Host check returned 403).
**The "no dropped connections" criterion was NOT met.** Under a single client sending requests as fast as it could while the route was added and removed, 0.05% to 10% of requests failed with `connection reset by peer` (about 10 of 17,000 with 150 ms between reloads, fresh connection per request, and `grace_period` set). Keep-alive reuse and reload rate change the count but never brought it to zero.
Not determined: whether this is Caddy's listener hand-off on reload or an artefact of the sandbox's network stack. Re-run on a real VM before relying on zero-drop reloads. Mitigations to evaluate in P2: coalesce reconciles so Caddy reloads only when the rendered config actually changes, and keep client retries safe for idempotent requests.
