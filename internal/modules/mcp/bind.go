package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/authz"
	"github.com/codeplo/codeplo/internal/crypto"
)

const (
	// SocketEnv optionally overrides the unix socket path; the default is <data-dir>/mcp.sock.
	SocketEnv = "CODEPLO_MCP_SOCKET"
	// TokenEnv holds the cpl_ API token whose scopes bind each MCP connection to a principal. It is read at accept
	// time, so rotating it takes effect on the next connection without a daemon restart. The value is never logged.
	TokenEnv = "CODEPLO_MCP_TOKEN"
)

// ErrDisabled is what Serve returns when the endpoint is switched off; the daemon turns it into a refusal message.
var ErrDisabled = errors.New("mcp module is disabled")

// SocketPath is where the daemon listens for MCP connections. The daemon and `codeplo mcp check` both compute it
// here so they agree on the path without either one owning the other's configuration.
func SocketPath(dataDir string) string {
	if v := os.Getenv(SocketEnv); v != "" {
		return v
	}
	return filepath.Join(dataDir, "mcp.sock")
}

// ResolvePrincipal resolves a cpl_ API token into the exact permission set the REST API's guard uses
// ((api.Server).authToken + rolePerms): the token's scopes, never exceeding its owner's current role, both
// validated against the authz registry. It reads the same api_tokens / users / role_permissions tables, so an
// admin's role edits and a token's own scopes are honoured here too -- nothing is a hardcoded, parallel list.
// A missing, expired, unknown or disabled token is an error; the daemon turns that into "no bound principal" and a
// refused connection.
func ResolvePrincipal(ctx context.Context, db *sql.DB, raw string) (map[string]bool, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "cpl_") {
		return nil, fmt.Errorf("no %s is set to a cpl_ API token", TokenEnv)
	}
	var id, scopes, uid string
	var exp sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT id,scopes,user_id,expires_at FROM api_tokens WHERE hash=?`, crypto.HashToken(raw)).
		Scan(&id, &scopes, &uid, &exp); err != nil {
		return nil, fmt.Errorf("the bound token is not a valid API token")
	}
	if exp.Valid {
		if t, err := time.Parse(time.RFC3339Nano, exp.String); err == nil && time.Now().After(t) {
			return nil, fmt.Errorf("the bound token has expired")
		}
	}
	var role string
	var disabled int
	if err := db.QueryRowContext(ctx, `SELECT role, disabled FROM users WHERE id=?`, uid).Scan(&role, &disabled); err != nil || disabled == 1 {
		return nil, fmt.Errorf("the bound token's account is unavailable")
	}
	owner := map[string]bool{}
	rows, err := db.QueryContext(ctx, `SELECT permission FROM role_permissions WHERE role=?`, role)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil && authz.Valid(p) {
			owner[p] = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	perms := map[string]bool{}
	for _, sc := range strings.Split(scopes, ",") {
		sc = strings.TrimSpace(sc)
		if authz.Valid(sc) && owner[sc] {
			perms[sc] = true
		}
	}
	_, _ = db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	return perms, nil
}

// Checker builds the CheckPerm closure for one bound principal. It is the single place a permission is validated:
// through authz.Valid (the same registry the REST API guard uses) and the principal's own permission set. No MCP
// tool can grant itself a permission the API would not grant, and no invented permission string is ever accepted.
func Checker(perms map[string]bool) func(string) error {
	return func(perm string) error {
		if !authz.Valid(perm) {
			return fmt.Errorf("mcp: permission %q is not in the authz registry", perm)
		}
		if !perms[perm] {
			return fmt.Errorf("mcp: the bound principal lacks permission %s", perm)
		}
		return nil
	}
}
