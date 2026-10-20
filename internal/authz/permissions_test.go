package authz

import "testing"

func TestRegistryAndRoles(t *testing.T) {
	if !Valid("users:manage") || !Valid(Public) || !Valid(Session) || Valid("nope") {
		t.Fatal("Valid wrong")
	}
	rp := RolePermissions()
	has := func(role, p string) bool {
		for _, x := range rp[role] {
			if x == p {
				return true
			}
		}
		return false
	}
	if !has("owner", "terminal:root") || has("admin", "terminal:root") || has("viewer", "env:write") || has("deployer", "users:manage") {
		t.Fatal("role matrix wrong")
	}
	for _, p := range All() {
		if Doc(p) == p {
			t.Errorf("permission %s has no description", p)
		}
	}
}
