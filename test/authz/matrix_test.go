// Package authz_test is the generated authorization matrix: every route in the API must declare exactly one
// valid permission. A route without one fails CI.
package authz_test

import (
	"testing"

	"github.com/codeplo/codeplo/internal/api"
	"github.com/codeplo/codeplo/internal/authz"
)

// publicAllowed is the closed list of unauthenticated routes.
var publicAllowed = map[string]bool{
	"GET /setup/state": true, "POST /setup/admin": true, "POST /setup/totp/enroll": true, "POST /setup/totp/verify": true,
	"POST /setup/verify-domain": true, "POST /setup/panel-domain": true, "GET /setup/operator-candidates": true,
	"POST /setup/operator": true, "POST /setup/complete": true, "POST /auth/login": true, "POST /auth/totp/verify": true,
	"POST /webhooks/github": true, "POST /hooks/deploy/{token}": true,
}

func TestEveryRouteDeclaresOnePermission(t *testing.T) {
	srv := api.New(nil, make([]byte, 32), "test", "test", "test") // route table only; no database needed
	seen := map[string]bool{}
	for _, r := range srv.Routes() {
		k := r.Method + " " + r.Path
		if seen[k] {
			t.Errorf("duplicate route %s", k)
		}
		seen[k] = true
		if r.Perm == "" || !authz.Valid(r.Perm) {
			t.Errorf("%s: missing or unknown permission %q", k, r.Perm)
		}
		if r.Handler == nil {
			t.Errorf("%s: no handler", k)
		}
		if r.Perm == authz.Public && !publicAllowed[k] {
			t.Errorf("%s is public but not in the allow-list", k)
		}
	}
	if len(seen) < 100 {
		t.Errorf("expected the full contract (>=100 routes), got %d", len(seen))
	}
}

func TestNoSecretRevealPermission(t *testing.T) {
	for _, p := range authz.All() {
		for _, bad := range []string{"reveal", "secret:read", "env:reveal"} {
			if p == bad {
				t.Fatalf("registry must not contain %s", p)
			}
		}
	}
}
