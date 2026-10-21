package policy

import "fmt"

// Control is one row of the effective-policy table (OpenAPI PolicyControl).
type Control struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Value   string  `json:"value"`
	Default string  `json:"default"`
	State   string  `json:"state"` // ok | warn | violation
	Note    *string `json:"note"`
}

// Describe renders the effective policy for a container as the table the UI shows. It judges nothing itself:
// state comes from the violations and warnings Enforce produced for the same container.
func Describe(c Container, res Result, o Options, envName string) []Control {
	state := func(ctl string) (string, *string) {
		for _, v := range res.Violations {
			if v.Control == ctl {
				m := v.Message
				return "violation", &m
			}
		}
		for _, w := range res.Warnings {
			if w.Control == ctl {
				m := w.Message
				return "warn", &m
			}
		}
		return "ok", nil
	}
	row := func(key, label, value, def string) Control {
		st, note := state(key)
		return Control{Key: key, Label: label, Value: value, Default: def, State: st, Note: note}
	}
	capVal := "drop ALL"
	if len(c.CapAdd) > 0 {
		capVal += ", add " + join(c.CapAdd)
	}
	user := c.User
	if user == "" {
		user = "image's USER"
	}
	sock := "rejected"
	if o.Trusted {
		sock = "allowed (trusted)"
	}
	ports := "none (Caddy reaches the container over the internal network)"
	if len(c.Ports) > 0 {
		ports = "published: " + join(c.Ports)
	}
	mem, cpu, pids := c.MemoryBytes, c.NanoCPUs, c.PidsLimit
	if mem <= 0 {
		mem = DefaultMemoryBytes
	}
	if cpu <= 0 {
		cpu = DefaultNanoCPUs
	}
	if pids <= 0 {
		pids = DefaultPidsLimit
	}
	out := []Control{
		row(CtlCapDrop, "Linux capabilities", capVal, "drop ALL"),
		row(CtlNoNewPrivs, "no-new-privileges", "enabled", "enabled"),
		row(CtlSeccomp, "Seccomp / AppArmor", "docker-default", "docker-default"),
		row(CtlUser, "User", user, "image's USER"),
		row(CtlPrivileged, "privileged / host pid / ipc / network / devices", "rejected", "rejected"),
		row(CtlDockerSock, "Docker socket mount", sock, "rejected"),
		row(CtlBindMounts, "Host bind mounts", "only under "+o.ResourceDir()+"/", "allowlist"),
		row(CtlPorts, "Published ports", ports, "none"),
		row(CtlLimits, "Memory / CPU / PIDs", fmt.Sprintf("%d MB · %.2g CPU · %d pids", mem>>20, float64(cpu)/1e9, pids), "256 MB · 0.5 CPU · 512 pids"),
		row(CtlNetwork, "Network", "per-environment bridge ("+envName+")", "per-environment bridge"),
	}
	// namespace and device violations are reported under the combined "privileged" row
	for _, v := range res.Violations {
		if v.Control == CtlNamespaces || v.Control == CtlDevices {
			note := v.Message
			out[4].State, out[4].Note = "violation", &note
		}
	}
	if o.Trusted {
		n := "Resource is marked trusted. This is root-equivalent."
		out[5].State, out[5].Note = "warn", &n
	}
	return out
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
