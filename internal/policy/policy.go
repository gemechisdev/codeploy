// Package policy is the workload isolation policy (plan 3.11), enforced in code. Every container the panel
// creates, and every service of a Compose file, passes Enforce before the runtime is asked to create anything.
// Enforce is the only constructor of Approved, and the runtime adapter only accepts Approved, so a container
// that violates the policy cannot be created by accident: it does not type-check.
package policy

import (
	"fmt"
	"sort"
	"strings"
)

// Controls are the stable keys of the policy table; they are what Violation.Control and PolicyControl.Key carry.
const (
	CtlCapDrop      = "cap_drop"
	CtlNoNewPrivs   = "no_new_privileges"
	CtlSeccomp      = "seccomp"
	CtlUser         = "user"
	CtlPrivileged   = "privileged"
	CtlDockerSock   = "docker_sock"
	CtlBindMounts   = "bind_mounts"
	CtlPorts        = "published_ports"
	CtlLimits       = "limits"
	CtlNetwork      = "network"
	CtlCompose      = "compose"
	CtlDevices      = "devices"
	CtlNamespaces   = "namespaces"
	defaultMemoryMB = 256
	defaultCPUs     = 0.5
	defaultPids     = 512
)

// Default limits applied when a resource does not set them (protects small hosts).
const (
	DefaultMemoryBytes int64 = defaultMemoryMB * 1024 * 1024
	DefaultNanoCPUs    int64 = int64(defaultCPUs * 1e9)
	DefaultPidsLimit   int64 = defaultPids
)

// Mount is one mount of a container. Type is "bind", "volume" or "tmpfs".
type Mount struct {
	Type     string
	Source   string
	Target   string
	ReadOnly bool
}

// Container is the requested shape of one container, before policy. Fields that the policy does not judge
// (Env, Cmd, Labels, Restart) ride along so the adapter has everything in one value.
type Container struct {
	Service string // Compose service name, or "app"
	Name    string
	Image   string
	Cmd     []string
	Env     []string
	Labels  map[string]string
	Restart string
	Aliases []string

	Privileged  bool
	PidMode     string
	IpcMode     string
	NetworkMode string
	UsernsMode  string
	Devices     []string
	CapAdd      []string
	CapDrop     []string
	SecurityOpt []string
	User        string
	Mounts      []Mount
	// Ports are published ports ("host:container" or "ip:host:container"). Empty by default.
	Ports []string

	MemoryBytes    int64
	NanoCPUs       int64
	PidsLimit      int64
	ReadonlyRootfs bool
	Network        string // the per-environment bridge; set by the caller, never from user input
}

// Options carries the per-resource facts the policy needs. Nothing here comes from a Compose file.
type Options struct {
	ResourceID string
	// VolumesRoot is where bind mounts may live: <VolumesRoot>/<ResourceID>/. Default /var/lib/codeplo/volumes.
	VolumesRoot string
	// DockerDataDir is always forbidden as a bind source. Default /var/lib/docker.
	DockerDataDir string
	// Trusted is the owner's explicit, audited per-resource switch for the Docker socket.
	Trusted bool
	// AllowCaps is the owner's per-resource allowlist of added capabilities (e.g. NET_BIND_SERVICE).
	AllowCaps []string
	// AllowPublish is the explicit opt-in for published ports (audit-flagged by the caller).
	AllowPublish bool
	// ExtraBinds are owner-approved bind sources outside the volumes root. They are still checked against the
	// forbidden list.
	ExtraBinds []string
	// AppArmor adds apparmor=docker-default where the host has AppArmor.
	AppArmor bool
	// AllowPrivilegeDrop omits no-new-privileges so official entrypoints (gosu/setpriv in Redis/Postgres/MySQL/nginx)
	// can drop root to their runtime user. Used for database templates and known images that cannot start under nnp.
	AllowPrivilegeDrop bool
	// EvalSymlinks resolves a path for the confinement check; nil uses the filesystem, tests inject a map.
	EvalSymlinks func(string) (string, error)
}

// Violation is one rejected control. Service is empty for a single-container resource.
type Violation struct {
	Service string `json:"service,omitempty"`
	Control string `json:"control"`
	Message string `json:"message"`
}

func (v Violation) String() string {
	if v.Service != "" && v.Service != "app" {
		return fmt.Sprintf("%s: %s", v.Service, v.Message)
	}
	return v.Message
}

// Approved is a container that passed the policy, with defaults applied. Only Enforce can build one.
type Approved struct{ c Container }

// Container returns a copy of the approved, defaulted container.
func (a Approved) Container() Container { return a.c }

// Valid reports whether the value came from Enforce (the zero value is not approved).
func (a Approved) Valid() bool { return a.c.Image != "" }

// Result is what Enforce returns. Approved is only usable when Violations is empty.
type Result struct {
	Approved   Approved
	Violations []Violation
	// Warnings never block (running as root, capability additions); the caller logs them in the deployment log.
	Warnings []Violation
	// Flags are audit flags for explicit opt-ins (trusted socket, published ports, extra binds, caps).
	Flags []string
}

// OK reports whether the container may be created.
func (r Result) OK() bool { return len(r.Violations) == 0 }

// Messages flattens violations for an error message.
func (r Result) Messages() []string {
	out := make([]string, 0, len(r.Violations))
	for _, v := range r.Violations {
		out = append(out, v.String())
	}
	return out
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Enforce validates c against the policy table and returns the defaulted container. It never mutates c.
func Enforce(c Container, o Options) Result {
	var res Result
	bad := func(ctl, msg string) { res.Violations = append(res.Violations, Violation{c.Service, ctl, msg}) }
	warn := func(ctl, msg string) { res.Warnings = append(res.Warnings, Violation{c.Service, ctl, msg}) }

	if strings.TrimSpace(c.Image) == "" {
		bad(CtlCompose, "container has no image")
	}

	// privileged, pid/ipc/network host, devices: rejected, no override in the MVP.
	if c.Privileged {
		bad(CtlPrivileged, "privileged containers are rejected")
	}
	if norm(c.PidMode) == "host" || strings.HasPrefix(norm(c.PidMode), "container:") {
		bad(CtlNamespaces, "pid: host (or sharing another container's pid namespace) is rejected")
	}
	if norm(c.IpcMode) == "host" || strings.HasPrefix(norm(c.IpcMode), "container:") {
		bad(CtlNamespaces, "ipc: host (or sharing another container's ipc namespace) is rejected")
	}
	if nm := norm(c.NetworkMode); nm != "" && nm != "default" && nm != "bridge" {
		bad(CtlNamespaces, "network_mode "+nm+" is rejected: workloads join the per-environment bridge network only")
	}
	if norm(c.UsernsMode) == "host" {
		bad(CtlNamespaces, "userns_mode: host is rejected")
	}
	if len(c.Devices) > 0 {
		bad(CtlDevices, "devices are rejected: "+strings.Join(c.Devices, ", "))
	}

	// capabilities: drop ALL, additions only from the owner's allowlist and never the dangerous set.
	caps, capBad := approveCaps(c.CapAdd, o.AllowCaps)
	for _, m := range capBad {
		bad(CtlCapDrop, m)
	}
	if len(caps) > 0 && len(capBad) == 0 {
		warn(CtlCapDrop, "added capabilities: "+strings.Join(caps, ", "))
		res.Flags = append(res.Flags, "cap_add:"+strings.Join(caps, ","))
	}

	// security options: seccomp/apparmor unconfined rejected; no-new-privileges is the default unless AllowPrivilegeDrop.
	sec, secBad := approveSecurityOpt(c.SecurityOpt, o.AppArmor, o.AllowPrivilegeDrop)
	for _, m := range secBad {
		bad(CtlSeccomp, m)
	}
	if o.AllowPrivilegeDrop {
		warn(CtlNoNewPrivs, "no-new-privileges disabled for this workload so the image entrypoint may drop privileges (database / known image)")
		res.Flags = append(res.Flags, "privilege_drop")
	}

	// user: the image's USER by default; root is a warning only.
	if u := strings.TrimSpace(c.User); u == "0" || u == "root" || strings.HasPrefix(u, "0:") || strings.HasPrefix(u, "root:") {
		warn(CtlUser, "the container runs as root; set a non-root user in the image or the resource's user setting")
	} else if u == "" {
		warn(CtlUser, "the container runs as the image's USER, which is root when the image does not set one")
	}

	// mounts: Docker socket, forbidden paths, confinement to the resource's volume directory.
	mounts, mBad, mFlags := approveMounts(c.Mounts, o)
	for _, v := range mBad {
		bad(v.Control, v.Message)
	}
	res.Flags = append(res.Flags, mFlags...)

	// published ports: none by default.
	if len(c.Ports) > 0 {
		if !o.AllowPublish {
			bad(CtlPorts, "published ports are rejected: Caddy reaches the container over the internal network ("+strings.Join(c.Ports, ", ")+")")
		} else {
			res.Flags = append(res.Flags, "published_ports:"+strings.Join(c.Ports, ","))
		}
	}

	// the network is always the per-environment bridge, chosen by the caller.
	if strings.TrimSpace(c.Network) == "" {
		bad(CtlNetwork, "no per-environment network was assigned")
	}

	if len(res.Violations) > 0 {
		return res
	}
	out := c
	out.Privileged = false
	out.PidMode, out.IpcMode, out.NetworkMode, out.UsernsMode = "", "", "", ""
	out.Devices = nil
	out.CapDrop = []string{"ALL"}
	out.CapAdd = caps
	out.SecurityOpt = sec
	out.Mounts = mounts
	out.Ports = append([]string(nil), c.Ports...)
	if out.MemoryBytes <= 0 {
		out.MemoryBytes = DefaultMemoryBytes
	}
	if out.NanoCPUs <= 0 {
		out.NanoCPUs = DefaultNanoCPUs
	}
	if out.PidsLimit <= 0 {
		out.PidsLimit = DefaultPidsLimit
	}
	res.Approved = Approved{c: out}
	sort.Strings(res.Flags)
	return res
}

// EnforceAll validates every container of a multi-service resource and aggregates the result. A single
// violation anywhere rejects the whole set, so nothing is created for a half-valid Compose file.
func EnforceAll(cs []Container, o Options) ([]Approved, Result) {
	var agg Result
	var out []Approved
	for _, c := range cs {
		r := Enforce(c, o)
		agg.Violations = append(agg.Violations, r.Violations...)
		agg.Warnings = append(agg.Warnings, r.Warnings...)
		agg.Flags = append(agg.Flags, r.Flags...)
		if r.OK() {
			out = append(out, r.Approved)
		}
	}
	if !agg.OK() {
		return nil, agg
	}
	return out, agg
}
