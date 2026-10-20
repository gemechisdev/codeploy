# ADR 0001: Single static binary
Status: accepted
Decision: ship `codeplod` and `codeplo` as static Go binaries (`CGO_ENABLED=0`) with the SPA embedded via `go:embed`.
Why: one artifact to verify and sign, no runtime dependencies on a small VPS, low memory.
Consequences: the web build must run before the Go build (`make build`); SQLite must be pure Go (see ADR 0002).
