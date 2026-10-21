package policy

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// forbiddenBinds can never be a bind source (or sit inside one); "/" is forbidden as an exact match only,
// since every path is under it.
var forbiddenBinds = []string{"/", "/etc", "/proc", "/sys", "/dev", "/var/run", "/run", "/boot", "/root", "/var/lib/docker", "/var/lib/containerd"}

var namedVolumeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

func (o Options) volumesRoot() string {
	if o.VolumesRoot == "" {
		return "/var/lib/codeplo/volumes"
	}
	return path.Clean(o.VolumesRoot)
}

// ResourceDir is the only directory tree a resource's bind mounts may live in.
func (o Options) ResourceDir() string { return path.Join(o.volumesRoot(), o.ResourceID) }

func (o Options) forbidden() []string {
	out := append([]string(nil), forbiddenBinds...)
	if d := strings.TrimSpace(o.DockerDataDir); d != "" {
		out = append(out, path.Clean(d))
	}
	return out
}

func within(p, root string) bool {
	if root == "/" {
		return p == "/"
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

func (o Options) resolve(p string) string {
	if o.EvalSymlinks != nil {
		if r, err := o.EvalSymlinks(p); err == nil {
			return path.Clean(r)
		}
		return path.Clean(p)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.ToSlash(r)
	}
	return path.Clean(p)
}

func isDockerSock(p string) bool {
	b := path.Base(p)
	return b == "docker.sock" || b == "containerd.sock" || b == "podman.sock"
}

// approveMounts checks every mount. Named volumes and tmpfs are fine; binds must sit under the resource's own
// directory, never touch a forbidden path (checked on the lexical and the symlink-resolved path), and the Docker
// socket is only allowed for a resource the owner marked trusted.
func approveMounts(in []Mount, o Options) ([]Mount, []Violation, []string) {
	var out []Mount
	var bad []Violation
	var flags []string
	add := func(ctl, msg string) { bad = append(bad, Violation{Control: ctl, Message: msg}) }
	for _, m := range in {
		t := strings.TrimSpace(m.Target)
		if t == "" || !strings.HasPrefix(t, "/") || strings.Contains(t, "..") {
			add(CtlBindMounts, "mount target "+quote(m.Target)+" must be an absolute path without \"..\"")
			continue
		}
		switch m.Type {
		case "tmpfs":
			out = append(out, m)
		case "volume":
			if !namedVolumeRe.MatchString(m.Source) {
				add(CtlBindMounts, "volume name "+quote(m.Source)+" is not a valid name")
				continue
			}
			out = append(out, m)
		case "bind":
			src := strings.TrimSpace(m.Source)
			if src == "" {
				add(CtlBindMounts, "bind mount has no source")
				continue
			}
			if !strings.HasPrefix(src, "/") {
				// relative sources resolve inside the resource directory (Compose "./data")
				src = path.Join(o.ResourceDir(), src)
			}
			if strings.Contains(src, "\x00") {
				add(CtlBindMounts, "bind source contains a NUL byte")
				continue
			}
			clean := path.Clean(src)
			if isDockerSock(clean) {
				if !o.Trusted {
					add(CtlDockerSock, "mounting the Docker socket is rejected: it is root on the host (only an owner can mark a resource trusted)")
					continue
				}
				flags = append(flags, "docker_socket")
				out = append(out, Mount{Type: "bind", Source: clean, Target: t, ReadOnly: m.ReadOnly})
				continue
			}
			real := o.resolve(clean)
			if isDockerSock(real) && !o.Trusted {
				add(CtlDockerSock, "mounting the Docker socket is rejected: it is root on the host")
				continue
			}
			if f := firstForbidden(o, clean, real); f != "" {
				add(CtlBindMounts, "bind source "+quote(m.Source)+" is rejected: "+f+" is a protected host path")
				continue
			}
			root := o.ResourceDir()
			if within(clean, root) && within(real, root) {
				out = append(out, Mount{Type: "bind", Source: clean, Target: t, ReadOnly: m.ReadOnly})
				continue
			}
			if o.extraBind(clean) {
				flags = append(flags, "bind_outside_volumes:"+clean)
				out = append(out, Mount{Type: "bind", Source: clean, Target: t, ReadOnly: m.ReadOnly})
				continue
			}
			add(CtlBindMounts, "bind source "+quote(m.Source)+" is rejected: host bind mounts are only allowed under "+root+"/")
		default:
			add(CtlBindMounts, "mount type "+quote(m.Type)+" is not supported")
		}
	}
	return out, bad, flags
}

func (o Options) extraBind(p string) bool {
	for _, e := range o.ExtraBinds {
		if within(p, path.Clean(e)) {
			return true
		}
	}
	return false
}

func firstForbidden(o Options, paths ...string) string {
	for _, p := range paths {
		// the panel's data directory holds the database and master key; only its volumes subtree is mountable
		if within(p, "/var/lib/codeplo") && !within(p, o.volumesRoot()) {
			return "/var/lib/codeplo"
		}
		for _, f := range o.forbidden() {
			if within(p, f) {
				return f
			}
		}
	}
	return ""
}

func quote(s string) string { return `"` + s + `"` }
