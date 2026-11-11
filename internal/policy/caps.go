package policy

import (
	"sort"
	"strings"
)

// neverCaps can never be added, even with an owner allowlist: each is a known container-escape or
// host-control primitive, and the plan has no override for them in the MVP.
var neverCaps = map[string]bool{
	"ALL": true, "SYS_ADMIN": true, "SYS_MODULE": true, "SYS_RAWIO": true, "SYS_BOOT": true,
	"SYS_PTRACE": true, "MAC_ADMIN": true, "MAC_OVERRIDE": true, "DAC_READ_SEARCH": true, "SYS_TIME": true,
	"BPF": true, "PERFMON": true, "NET_ADMIN": true, "SYSLOG": true, "SYS_CHROOT_ESCAPE": true,
}

// DefaultCapAllowlist is what an owner may grant per resource through the UI (the mock and the API validation
// use the same list): small, unprivileged-port and ownership capabilities only.
var DefaultCapAllowlist = []string{"NET_BIND_SERVICE", "CHOWN", "SETUID", "SETGID"}

func normCap(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	return strings.TrimPrefix(c, "CAP_")
}

// approveCaps returns the deduplicated, sorted additions and the reasons for any rejection.
func approveCaps(add, allow []string) ([]string, []string) {
	allowed := map[string]bool{}
	for _, a := range allow {
		allowed[normCap(a)] = true
	}
	seen := map[string]bool{}
	var out, bad []string
	for _, c := range add {
		n := normCap(c)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		switch {
		case neverCaps[n]:
			bad = append(bad, "capability "+n+" can never be added")
		case !allowed[n]:
			bad = append(bad, "capability "+n+" is not on this resource's allowlist (the owner can grant it, audited)")
		default:
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, bad
}

// approveSecurityOpt validates --security-opt values. Accepted: no-new-privileges (true), apparmor=docker-default.
// Rejected: any "unconfined", label=disable, seccomp=<anything> (Docker's default profile only), no-new-privileges=false.
// The returned list always carries no-new-privileges:true, plus apparmor=docker-default when the host has it.
func approveSecurityOpt(in []string, apparmor, allowPrivilegeDrop bool) ([]string, []string) {
	var bad []string
	for _, raw := range in {
		o := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), " ", ""))
		switch {
		case o == "":
		case strings.Contains(o, "unconfined"):
			bad = append(bad, "security_opt "+raw+" is rejected: unconfined seccomp/AppArmor profiles are not allowed")
		case strings.HasPrefix(o, "label=disable") || strings.HasPrefix(o, "label:disable"):
			bad = append(bad, "security_opt "+raw+" is rejected: SELinux labelling cannot be disabled")
		case strings.HasPrefix(o, "seccomp"):
			bad = append(bad, "security_opt "+raw+" is rejected: only Docker's default seccomp profile is allowed")
		case strings.HasPrefix(o, "systempaths"):
			bad = append(bad, "security_opt "+raw+" is rejected: masked/read-only system paths cannot be changed")
		case strings.HasPrefix(o, "no-new-privileges"):
			if (strings.HasSuffix(o, "=false") || strings.HasSuffix(o, ":false")) && !allowPrivilegeDrop {
				bad = append(bad, "no-new-privileges cannot be disabled")
			}
		case o == "apparmor=docker-default" || o == "apparmor:docker-default":
		case strings.HasPrefix(o, "apparmor"):
			bad = append(bad, "security_opt "+raw+" is rejected: only the docker-default AppArmor profile is allowed")
		default:
			bad = append(bad, "security_opt "+raw+" is not supported")
		}
	}
	var out []string
	if !allowPrivilegeDrop {
		out = append(out, "no-new-privileges:true")
	}
	if apparmor {
		out = append(out, "apparmor=docker-default")
	}
	return out, bad
}
