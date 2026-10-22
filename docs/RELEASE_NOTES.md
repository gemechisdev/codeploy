# Release notes (development)

## Known measurement gaps (do not silent-tune)

- **Idle RSS (T30):** Profile-A budget is 50 MB; measured idle RSS was **78.8 MB**. Re-measure on the reference VM in CI before publishing a number operators will rely on.
- **Caddy zero-drop spike (T31):** Spike A remains **unmet** (flaky reload timing). The assertion stays strict.

## Migrations

- Head: **0008_extensions** (extension registry, tokens, subscriptions, deliveries).

## Extensions / MCP

- No OpenAPI routes (see `docs/DECISIONS.md` T53).
- CLI: `codeplo extensions …`
- Feature flags: `extensions.outbound`, `mcp.server`.
