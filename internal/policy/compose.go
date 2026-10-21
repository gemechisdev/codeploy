package policy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// supportedServiceKeys is the allowlist of Compose service keys. Anything else is rejected with its name, so an
// unsupported or dangerous key (cgroup_parent, sysctls, volumes_from, extra_hosts, ...) never silently passes.
// Keys that are on the list but judged by the policy (privileged, pid, devices, ports, ...) become Violations.
var supportedServiceKeys = map[string]bool{
	"image": true, "command": true, "entrypoint": true, "environment": true, "ports": true, "expose": true,
	"volumes": true, "restart": true, "user": true, "working_dir": true, "healthcheck": true, "labels": true,
	"depends_on": true, "cap_add": true, "cap_drop": true, "security_opt": true, "privileged": true, "pid": true,
	"ipc": true, "network_mode": true, "devices": true, "mem_limit": true, "cpus": true, "pids_limit": true,
	"deploy": true, "read_only": true, "tmpfs": true, "networks": true, "init": true, "stop_grace_period": true,
	"container_name": true, "userns_mode": true, "hostname": true,
}

// ParseCompose parses a Compose file and returns one Container per service, ready for EnforceAll. Parse-level
// problems (unknown keys, build:, env_file:) come back as violations with control "compose". The caller sets
// Name, Network and Labels on each returned container (they are identity, not user input).
func ParseCompose(data []byte) ([]Container, []Violation) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, []Violation{{Control: CtlCompose, Message: "compose file is not valid YAML: " + err.Error()}}
	}
	var bad []Violation
	fail := func(svc, msg string) { bad = append(bad, Violation{Service: svc, Control: CtlCompose, Message: msg}) }
	for k := range doc {
		switch k {
		case "services", "version", "name", "volumes", "networks":
		default:
			fail("", "top-level key "+quote(k)+" is not supported")
		}
	}
	if nets, ok := doc["networks"].(map[string]any); ok {
		for n, v := range nets {
			if m, ok := v.(map[string]any); ok {
				if ext, has := m["external"]; has && ext != false {
					fail("", "network "+quote(n)+" is external: services join the per-environment network only")
				}
				if d, has := m["driver"]; has && fmt.Sprint(d) != "bridge" {
					fail("", "network "+quote(n)+" uses driver "+fmt.Sprint(d)+": only the per-environment bridge is allowed")
				}
			}
		}
	}
	svcs, ok := doc["services"].(map[string]any)
	if !ok || len(svcs) == 0 {
		fail("", "compose file has no services")
		return nil, bad
	}
	names := make([]string, 0, len(svcs))
	for n := range svcs {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Container
	for _, name := range names {
		raw, ok := svcs[name].(map[string]any)
		if !ok {
			fail(name, "service is not a mapping")
			continue
		}
		c := Container{Service: name, Restart: "unless-stopped"}
		for k := range raw {
			if k == "build" {
				fail(name, "build: is not supported in Compose resources; build the image and reference it with image:")
			} else if k == "env_file" {
				fail(name, "env_file: is not supported; use the resource's environment editor")
			} else if !supportedServiceKeys[k] {
				fail(name, "key "+quote(k)+" is not supported")
			}
		}
		c.Image = str(raw["image"])
		c.Privileged = truthy(raw["privileged"])
		c.PidMode = str(raw["pid"])
		c.IpcMode = str(raw["ipc"])
		c.NetworkMode = str(raw["network_mode"])
		c.UsernsMode = str(raw["userns_mode"])
		c.User = str(raw["user"])
		c.Devices = strList(raw["devices"])
		c.CapAdd = strList(raw["cap_add"])
		c.CapDrop = strList(raw["cap_drop"])
		c.SecurityOpt = strList(raw["security_opt"])
		c.ReadonlyRootfs = truthy(raw["read_only"])
		c.Cmd = cmdList(raw["command"])
		c.Env = envList(raw["environment"])
		if r := str(raw["restart"]); r != "" {
			c.Restart = r
		}
		if v := str(raw["mem_limit"]); v != "" {
			if n, err := parseBytes(v); err == nil {
				c.MemoryBytes = n
			} else {
				fail(name, "mem_limit "+quote(v)+" is not understood")
			}
		}
		if v := str(raw["cpus"]); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				c.NanoCPUs = int64(f * 1e9)
			} else {
				fail(name, "cpus "+quote(v)+" is not a number")
			}
		}
		if v := str(raw["pids_limit"]); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				c.PidsLimit = n
			}
		}
		if d, ok := raw["deploy"].(map[string]any); ok {
			if res, ok := d["resources"].(map[string]any); ok {
				if lim, ok := res["limits"].(map[string]any); ok {
					if v := str(lim["memory"]); v != "" {
						if n, err := parseBytes(v); err == nil {
							c.MemoryBytes = n
						}
					}
					if v := str(lim["cpus"]); v != "" {
						if f, err := strconv.ParseFloat(v, 64); err == nil {
							c.NanoCPUs = int64(f * 1e9)
						}
					}
					if v := str(lim["pids"]); v != "" {
						if n, err := strconv.ParseInt(v, 10, 64); err == nil {
							c.PidsLimit = n
						}
					}
				}
			}
		}
		if t, ok := raw["tmpfs"]; ok {
			for _, p := range strList(t) {
				c.Mounts = append(c.Mounts, Mount{Type: "tmpfs", Target: strings.SplitN(p, ":", 2)[0]})
			}
		}
		ms, mbad := parseVolumes(raw["volumes"])
		c.Mounts = append(c.Mounts, ms...)
		for _, m := range mbad {
			fail(name, m)
		}
		ps, pbad := parsePorts(raw["ports"])
		c.Ports = ps
		for _, m := range pbad {
			fail(name, m)
		}
		out = append(out, c)
	}
	return out, bad
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true") || x == "1"
	}
	return false
}

func strList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s := str(e); s != "" {
				out = append(out, s)
			}
		}
	case string:
		if s := strings.TrimSpace(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func cmdList(v any) []string {
	if s, ok := v.(string); ok {
		return strings.Fields(s)
	}
	return strList(v)
}

func envList(v any) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, k+"="+str(x[k]))
		}
	case []any:
		out = strList(x)
	}
	return out
}

func parseBytes(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := int64(1)
	// longest suffix first: "mb" must win over "b"
	for _, u := range []struct {
		suf string
		m   int64
	}{{"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10}, {"b", 1}} {
		if strings.HasSuffix(s, u.suf) {
			s, mult = strings.TrimSuffix(s, u.suf), u.m
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n * mult, err
}

// parseVolumes understands the short ("src:dst[:ro]") and long (type/source/target) Compose syntax.
func parseVolumes(v any) ([]Mount, []string) {
	var out []Mount
	var bad []string
	list, _ := v.([]any)
	for _, e := range list {
		switch x := e.(type) {
		case string:
			parts := strings.Split(x, ":")
			switch len(parts) {
			case 1:
				bad = append(bad, "anonymous volume "+quote(x)+" is not supported; name it")
			default:
				m := Mount{Source: parts[0], Target: parts[1]}
				m.ReadOnly = len(parts) > 2 && strings.Contains(parts[2], "ro")
				if strings.HasPrefix(m.Source, "/") || strings.HasPrefix(m.Source, ".") || strings.HasPrefix(m.Source, "~") {
					m.Type = "bind"
					if strings.HasPrefix(m.Source, "~") {
						bad = append(bad, "bind source "+quote(m.Source)+" is rejected: home-relative paths are not allowed")
						continue
					}
				} else {
					m.Type = "volume"
				}
				out = append(out, m)
			}
		case map[string]any:
			m := Mount{Type: str(x["type"]), Source: str(x["source"]), Target: str(x["target"]), ReadOnly: truthy(x["read_only"])}
			if m.Type == "" {
				m.Type = "volume"
			}
			if m.Type == "bind" && strings.HasPrefix(m.Source, "~") {
				bad = append(bad, "bind source "+quote(m.Source)+" is rejected: home-relative paths are not allowed")
				continue
			}
			out = append(out, m)
		default:
			bad = append(bad, "volume entry is not understood")
		}
	}
	return out, bad
}

// parsePorts returns published ports only: a bare container port ("80") is not published and is dropped.
func parsePorts(v any) ([]string, []string) {
	var out, bad []string
	list, _ := v.([]any)
	for _, e := range list {
		switch x := e.(type) {
		case string:
			if strings.Contains(x, ":") {
				out = append(out, x)
			}
		case int:
		case map[string]any:
			if pub := str(x["published"]); pub != "" {
				out = append(out, pub+":"+str(x["target"]))
			}
		default:
			bad = append(bad, "port entry is not understood")
		}
	}
	return out, bad
}
