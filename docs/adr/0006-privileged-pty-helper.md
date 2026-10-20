# ADR 0006: host shells run through a root-side PTY helper, not from the daemon

Status: accepted (implemented, not yet verified on a real host)

## Context

Plan P3 requires a host shell that runs as the operator user. `codeplod` runs as the unprivileged `codeplo`
user with `NoNewPrivileges=true` and `ProtectSystem=strict`. Two things follow:

1. The daemon cannot switch to another uid, so it cannot start the operator's shell itself.
2. Any shell spawned from the daemon's process tree inherits `NoNewPrivileges`, so `sudo` would fail inside the
   shell. A host shell for a sudo operator that cannot sudo defeats its purpose.

Granting the daemon `CAP_SETUID` and dropping `NoNewPrivileges` would widen the panel's own attack surface to fix
a problem that only the shell has.

## Decision

A small helper, `codeplo pty-helper`, runs as root outside the daemon's process tree, started per connection by a
socket-activated unit (`codeplo-pty.socket` + `codeplo-pty@.service`, `Accept=yes`). The socket is
`/run/codeplo-pty/pty.sock`, mode 0660, group `codeplo`. It lives in its own directory because
`/run/codeplo` belongs to the Caddy unit's `RuntimeDirectory` and is removed when that unit stops.

The helper does not trust the daemon:

- It checks the peer uid with `SO_PEERCRED` and accepts only the `codeplo` service user.
- `terminal.Authorize` allows only an account with uid 1000..65533, a real login shell and membership of `sudo`
  or `wheel` (the same predicate the setup wizard uses for operator candidates), or root.
- Root is allowed only when the host owner created `/etc/codeplo/allow-root`, a root-owned file the daemon
  cannot write. The panel setting `terminal.allow_root`, the `terminal:root` permission and a fresh step-up are
  all still required on the daemon side; this file is an independent switch on the host side.
- On any socket error the helper kills the shell's process group (`Setsid`, then `kill(-pgid)`), so closing the
  browser tab ends the shell.

The daemon talks to it with length-prefixed frames (`internal/modules/terminal/frames.go`): data, resize, exit.

## Consequences

- A compromised daemon can request a shell for a sudo user, which it could not do before. It cannot reach root
  unless the host owner enabled `allow-root`, and it cannot reach non-sudo accounts. This is recorded in the
  threat model as accepted residual risk for the privilege the operator was always meant to have.
- The host shell is unavailable until the helper units are installed; the daemon reports this instead of
  failing silently.
- Container shells do not use the helper; they use the Docker Engine API, which needs `docker` group membership
  for the daemon (root-equivalent, granted by the installer only when Docker is present).
