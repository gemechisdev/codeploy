// Package authz holds the permission registry (no secret-reveal permission exists) and route guards.
package authz

import "strings"

const (
	Public  = "public"  // no authentication
	Session = "session" // any authenticated principal
)

var all = strings.Fields(`resource:read resource:write resource:delete resource:control deploy:create deploy:cancel deploy:rollback env:read env:write
domain:write project:write terminal:host terminal:root terminal:container hardening:read hardening:apply edge:read edge:reconcile
git:write hook:manage audit:read users:manage tokens:manage modules:manage flags:manage system:read system:metrics system:cleanup
notify:manage backup:manage backup:restore template:deploy`)

var docs = map[string]string{
	"resource:read": "View projects, resources and deployments", "resource:write": "Create and edit resources",
	"resource:delete": "Delete resources", "resource:control": "Start, stop and restart resources",
	"deploy:create": "Trigger deployments", "deploy:cancel": "Cancel running deployments", "deploy:rollback": "Roll back to a retained image",
	"env:read": "List environment variables (secret values are never returned)", "env:write": "Add, replace and delete variables",
	"domain:write": "Add and remove domains", "project:write": "Create projects and environments",
	"terminal:host": "Open the host shell as the operator user", "terminal:root": "Open a root shell (step-up required)",
	"terminal:container": "Open a container shell", "hardening:read": "View audit, plans and preflight", "hardening:apply": "Apply, confirm and revert hardening changes",
	"edge:read": "View proxy status and certificates", "edge:reconcile": "Force a Caddy reconcile", "git:write": "Manage Git sources and keys",
	"hook:manage": "Manage webhooks and deploy hooks", "audit:read": "Read and verify the audit log", "users:manage": "Create and edit users",
	"tokens:manage": "Create and revoke API tokens", "modules:manage": "Enable or disable modules", "flags:manage": "Change feature flags and settings",
	"system:read": "View host status", "system:metrics": "View metric history", "system:cleanup": "Run Docker cleanup",
	"notify:manage": "Manage notification channels and alerts", "backup:manage": "Manage backup schedules and runs", "backup:restore": "Restore backups (step-up)",
	"template:deploy": "Deploy catalog templates",
}

func All() []string { return append([]string(nil), all...) }

func Doc(p string) string {
	if d, ok := docs[p]; ok {
		return d
	}
	return p
}

// Valid reports whether p is a registered permission or a pseudo-permission.
func Valid(p string) bool {
	if p == Public || p == Session {
		return true
	}
	for _, x := range all {
		if x == p {
			return true
		}
	}
	return false
}

func RolePermissions() map[string][]string {
	admin := []string{}
	for _, p := range all {
		if p != "terminal:root" {
			admin = append(admin, p)
		}
	}
	return map[string][]string{
		"owner":    All(),
		"admin":    admin,
		"deployer": strings.Fields("resource:read resource:control deploy:create deploy:cancel deploy:rollback env:read env:write system:read"),
		"viewer":   strings.Fields("resource:read env:read hardening:read edge:read system:read system:metrics"),
	}
}
