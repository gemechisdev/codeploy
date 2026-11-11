package policy

import (
	"strings"
	"testing"
)

func opts() Options {
	return Options{ResourceID: "res_1", VolumesRoot: "/var/lib/codeplo/volumes", DockerDataDir: "/var/lib/docker",
		EvalSymlinks: func(p string) (string, error) { return p, nil }}
}

func base() Container {
	return Container{Service: "app", Image: "ghcr.io/acme/app:1", Network: "cpl-env-1"}
}

func mustReject(t *testing.T, name string, c Container, o Options, want string) {
	t.Helper()
	r := Enforce(c, o)
	if r.OK() {
		t.Fatalf("%s: expected a violation, container was approved", name)
	}
	if r.Approved.Valid() {
		t.Fatalf("%s: a rejected container produced an Approved value", name)
	}
	joined := strings.Join(r.Messages(), " | ")
	if !strings.Contains(joined, want) {
		t.Fatalf("%s: violation %q does not mention %q", name, joined, want)
	}
}

func TestDefaultsAreApplied(t *testing.T) {
	r := Enforce(base(), opts())
	if !r.OK() {
		t.Fatalf("a plain container must pass: %v", r.Messages())
	}
	c := r.Approved.Container()
	if len(c.CapDrop) != 1 || c.CapDrop[0] != "ALL" {
		t.Fatalf("cap_drop must be ALL, got %v", c.CapDrop)
	}
	found := false
	for _, s := range c.SecurityOpt {
		if s == "no-new-privileges:true" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no-new-privileges must be set: %v", c.SecurityOpt)
	}
	if c.MemoryBytes != DefaultMemoryBytes || c.NanoCPUs != DefaultNanoCPUs || c.PidsLimit != DefaultPidsLimit {
		t.Fatalf("limit defaults missing: %d %d %d", c.MemoryBytes, c.NanoCPUs, c.PidsLimit)
	}
	if len(c.Ports) != 0 {
		t.Fatalf("no ports may be published by default")
	}
}


func TestAllowPrivilegeDropOmitsNoNewPrivileges(t *testing.T) {
	c := base()
	o := opts()
	o.AllowPrivilegeDrop = true
	a := Enforce(c, o).Approved.Container()
	for _, s := range a.SecurityOpt {
		if s == "no-new-privileges:true" {
			t.Fatalf("AllowPrivilegeDrop must omit no-new-privileges: %v", a.SecurityOpt)
		}
	}
}

func TestExplicitLimitsKept(t *testing.T) {
	c := base()
	c.MemoryBytes, c.NanoCPUs, c.PidsLimit = 64<<20, 2e9, 99
	a := Enforce(c, opts()).Approved.Container()
	if a.MemoryBytes != 64<<20 || a.NanoCPUs != 2e9 || a.PidsLimit != 99 {
		t.Fatalf("explicit limits must survive: %+v", a)
	}
}

func TestEveryViolationIsRejected(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Container)
		o    func(*Options)
		want string
	}{
		{"privileged", func(c *Container) { c.Privileged = true }, nil, "privileged"},
		{"pid host", func(c *Container) { c.PidMode = "host" }, nil, "pid: host"},
		{"ipc host", func(c *Container) { c.IpcMode = "host" }, nil, "ipc: host"},
		{"network host", func(c *Container) { c.NetworkMode = "host" }, nil, "network_mode host"},
		{"network container", func(c *Container) { c.NetworkMode = "container:abc" }, nil, "network_mode"},
		{"userns host", func(c *Container) { c.UsernsMode = "host" }, nil, "userns_mode"},
		{"devices", func(c *Container) { c.Devices = []string{"/dev/sda:/dev/sda"} }, nil, "devices"},
		{"seccomp unconfined", func(c *Container) { c.SecurityOpt = []string{"seccomp=unconfined"} }, nil, "unconfined"},
		{"apparmor unconfined", func(c *Container) { c.SecurityOpt = []string{"apparmor=unconfined"} }, nil, "unconfined"},
		{"seccomp custom", func(c *Container) { c.SecurityOpt = []string{"seccomp=/tmp/p.json"} }, nil, "default seccomp"},
		{"nnp off", func(c *Container) { c.SecurityOpt = []string{"no-new-privileges=false"} }, nil, "no-new-privileges"},
		{"label disable", func(c *Container) { c.SecurityOpt = []string{"label=disable"} }, nil, "SELinux"},
		{"cap SYS_ADMIN", func(c *Container) { c.CapAdd = []string{"SYS_ADMIN"} }, func(o *Options) { o.AllowCaps = []string{"SYS_ADMIN"} }, "can never be added"},
		{"cap ALL", func(c *Container) { c.CapAdd = []string{"ALL"} }, nil, "can never be added"},
		{"cap not allowlisted", func(c *Container) { c.CapAdd = []string{"CHOWN"} }, nil, "allowlist"},
		{"docker socket", func(c *Container) {
			c.Mounts = []Mount{{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"}}
		}, nil, "Docker socket"},
		{"docker socket alt path", func(c *Container) {
			c.Mounts = []Mount{{Type: "bind", Source: "/run/docker.sock", Target: "/s"}}
		}, nil, "Docker socket"},
		{"bind root", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/", Target: "/host"}} }, nil, "protected host path"},
		{"bind etc", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/etc", Target: "/e"}} }, nil, "/etc"},
		{"bind etc child", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/etc/shadow", Target: "/e"}} }, nil, "/etc"},
		{"bind proc", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/proc", Target: "/p"}} }, nil, "/proc"},
		{"bind sys", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/sys/fs", Target: "/p"}} }, nil, "/sys"},
		{"bind dev", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/dev", Target: "/p"}} }, nil, "/dev"},
		{"bind var run", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/var/run", Target: "/p"}} }, nil, "/var/run"},
		{"bind docker data", func(c *Container) {
			c.Mounts = []Mount{{Type: "bind", Source: "/var/lib/docker/volumes", Target: "/p"}}
		}, nil, "/var/lib/docker"},
		{"bind custom docker data", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/data/docker/x", Target: "/p"}} }, func(o *Options) { o.DockerDataDir = "/data/docker" }, "/data/docker"},
		{"bind panel data dir", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/var/lib/codeplo", Target: "/p"}} }, func(o *Options) { o.ExtraBinds = []string{"/var/lib/codeplo"} }, "protected host path"},
		{"bind outside volumes", func(c *Container) { c.Mounts = []Mount{{Type: "bind", Source: "/home/user", Target: "/p"}} }, nil, "only allowed under"},
		{"bind other resource", func(c *Container) {
			c.Mounts = []Mount{{Type: "bind", Source: "/var/lib/codeplo/volumes/res_2/data", Target: "/p"}}
		}, nil, "only allowed under"},
		{"bind traversal", func(c *Container) {
			c.Mounts = []Mount{{Type: "bind", Source: "/var/lib/codeplo/volumes/res_1/../res_2", Target: "/p"}}
		}, nil, "only allowed under"},
		{"bind symlink escape", func(c *Container) {
			c.Mounts = []Mount{{Type: "bind", Source: "/var/lib/codeplo/volumes/res_1/link", Target: "/p"}}
		}, func(o *Options) {
			o.EvalSymlinks = func(p string) (string, error) {
				if p == "/var/lib/codeplo/volumes/res_1/link" {
					return "/etc", nil
				}
				return p, nil
			}
		}, "/etc"},
		{"relative target", func(c *Container) { c.Mounts = []Mount{{Type: "volume", Source: "data", Target: "data"}} }, nil, "absolute"},
		{"published ports", func(c *Container) { c.Ports = []string{"8080:80"} }, nil, "published ports"},
		{"no network", func(c *Container) { c.Network = "" }, nil, "per-environment network"},
		{"no image", func(c *Container) { c.Image = "" }, nil, "no image"},
	}
	for _, tc := range cases {
		c, o := base(), opts()
		tc.mut(&c)
		if tc.o != nil {
			tc.o(&o)
		}
		mustReject(t, tc.name, c, o, tc.want)
	}
}

func TestAllowedShapes(t *testing.T) {
	c := base()
	c.CapAdd = []string{"cap_net_bind_service"}
	c.Mounts = []Mount{
		{Type: "bind", Source: "/var/lib/codeplo/volumes/res_1/data", Target: "/data"},
		{Type: "bind", Source: "./uploads", Target: "/up"},
		{Type: "volume", Source: "cache", Target: "/cache"},
		{Type: "tmpfs", Target: "/tmp"},
	}
	o := opts()
	o.AllowCaps = []string{"NET_BIND_SERVICE"}
	r := Enforce(c, o)
	if !r.OK() {
		t.Fatalf("expected approval: %v", r.Messages())
	}
	a := r.Approved.Container()
	if a.CapAdd[0] != "NET_BIND_SERVICE" || a.CapDrop[0] != "ALL" {
		t.Fatalf("caps: add=%v drop=%v", a.CapAdd, a.CapDrop)
	}
	if a.Mounts[1].Source != "/var/lib/codeplo/volumes/res_1/uploads" {
		t.Fatalf("relative bind must resolve into the resource dir, got %s", a.Mounts[1].Source)
	}
}

func TestDockerSocketOnlyWhenTrusted(t *testing.T) {
	c := base()
	c.Mounts = []Mount{{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"}}
	o := opts()
	o.Trusted = true
	r := Enforce(c, o)
	if !r.OK() {
		t.Fatalf("trusted resource may mount the socket: %v", r.Messages())
	}
	if len(r.Flags) == 0 || r.Flags[0] != "docker_socket" {
		t.Fatalf("the socket mount must be audit-flagged: %v", r.Flags)
	}
	// trust does not unlock any other forbidden path
	c.Mounts = []Mount{{Type: "bind", Source: "/etc", Target: "/e"}}
	if Enforce(c, o).OK() {
		t.Fatal("trusted must not unlock /etc")
	}
}

func TestPublishedPortsOptIn(t *testing.T) {
	c := base()
	c.Ports = []string{"127.0.0.1:9000:9000"}
	o := opts()
	o.AllowPublish = true
	r := Enforce(c, o)
	if !r.OK() {
		t.Fatalf("opt-in must pass: %v", r.Messages())
	}
	if len(r.Flags) == 0 || !strings.HasPrefix(r.Flags[0], "published_ports") {
		t.Fatalf("published ports must be audit-flagged: %v", r.Flags)
	}
}

func TestSecurityOptAlwaysCarriesNNP(t *testing.T) {
	c := base()
	c.SecurityOpt = []string{"apparmor=docker-default"}
	o := opts()
	o.AppArmor = true
	a := Enforce(c, o).Approved.Container()
	if len(a.SecurityOpt) != 2 || a.SecurityOpt[0] != "no-new-privileges:true" || a.SecurityOpt[1] != "apparmor=docker-default" {
		t.Fatalf("security opts: %v", a.SecurityOpt)
	}
}

func TestRootUserWarns(t *testing.T) {
	c := base()
	c.User = "root"
	r := Enforce(c, opts())
	if !r.OK() || len(r.Warnings) == 0 {
		t.Fatalf("root must warn, not reject: ok=%v warnings=%v", r.OK(), r.Warnings)
	}
}

func TestComposeEveryServiceIsChecked(t *testing.T) {
	file := `
services:
  web:
    image: nginx:1
    ports: ["8080:80"]
  db:
    image: postgres:16
    privileged: true
`
	cs, bad := ParseCompose([]byte(file))
	if len(bad) != 0 {
		t.Fatalf("parse: %v", bad)
	}
	for i := range cs {
		cs[i].Network = "cpl-env-1"
	}
	_, res := EnforceAll(cs, opts())
	if res.OK() {
		t.Fatal("a compose file with a privileged service and a published port must be rejected")
	}
	var privSvc, portSvc bool
	for _, v := range res.Violations {
		if v.Service == "db" && v.Control == CtlPrivileged {
			privSvc = true
		}
		if v.Service == "web" && v.Control == CtlPorts {
			portSvc = true
		}
	}
	if !privSvc || !portSvc {
		t.Fatalf("both services must be reported separately: %+v", res.Violations)
	}
}

func TestComposeRejectsDangerousAndUnsupportedKeys(t *testing.T) {
	cases := map[string]string{
		"build":         "services:\n  a:\n    build: .\n",
		"env_file":      "services:\n  a:\n    image: x\n    env_file: .env\n",
		"cgroup_parent": "services:\n  a:\n    image: x\n    cgroup_parent: /\n",
		"sysctls":       "services:\n  a:\n    image: x\n    sysctls: {net.ipv4.ip_forward: 1}\n",
		"volumes_from":  "services:\n  a:\n    image: x\n    volumes_from: [b]\n",
		"external net":  "services:\n  a:\n    image: x\nnetworks:\n  n:\n    external: true\n",
		"no services":   "version: '3'\n",
		"toplevel":      "services:\n  a:\n    image: x\nconfigs: {}\n",
		"anon volume":   "services:\n  a:\n    image: x\n    volumes: ['/data']\n",
		"home bind":     "services:\n  a:\n    image: x\n    volumes: ['~/x:/x']\n",
		"invalid yaml":  "services: [",
	}
	for name, f := range cases {
		cs, bad := ParseCompose([]byte(f))
		if len(bad) == 0 {
			for i := range cs {
				cs[i].Network = "n"
			}
			if _, r := EnforceAll(cs, opts()); r.OK() {
				t.Errorf("%s: compose file was accepted", name)
			}
		}
	}
}

func TestComposeEnforcesTheSameMountAndSocketRules(t *testing.T) {
	file := `
services:
  a:
    image: x
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
  b:
    image: x
    volumes:
      - type: bind
        source: /etc
        target: /host-etc
  c:
    image: x
    pid: host
    security_opt: ["seccomp:unconfined"]
    devices: ["/dev/kvm"]
    cap_add: [SYS_ADMIN]
    network_mode: host
    ipc: host
`
	cs, bad := ParseCompose([]byte(file))
	if len(bad) != 0 {
		t.Fatalf("parse: %v", bad)
	}
	for i := range cs {
		cs[i].Network = "n"
	}
	_, r := EnforceAll(cs, opts())
	got := map[string]bool{}
	for _, v := range r.Violations {
		got[v.Service+"/"+v.Control] = true
	}
	for _, k := range []string{"a/" + CtlDockerSock, "b/" + CtlBindMounts, "c/" + CtlNamespaces, "c/" + CtlSeccomp, "c/" + CtlDevices, "c/" + CtlCapDrop} {
		if !got[k] {
			t.Errorf("missing violation %s in %+v", k, r.Violations)
		}
	}
}

func TestComposeLimitsAndValidFile(t *testing.T) {
	file := `
services:
  web:
    image: nginx:1
    mem_limit: 128m
    cpus: 0.25
    pids_limit: 100
    environment:
      A: "1"
    volumes:
      - data:/var/data
      - ./site:/usr/share/nginx/html:ro
    expose: ["80"]
    ports: ["80"]
`
	cs, bad := ParseCompose([]byte(file))
	if len(bad) != 0 {
		t.Fatalf("parse: %v", bad)
	}
	cs[0].Network = "n"
	ap, res := EnforceAll(cs, opts())
	if !res.OK() || len(ap) != 1 {
		t.Fatalf("valid compose must pass: %v", res.Messages())
	}
	c := ap[0].Container()
	if c.MemoryBytes != 128<<20 || c.NanoCPUs != 250000000 || c.PidsLimit != 100 {
		t.Fatalf("limits not parsed: %+v", c)
	}
	if len(c.Ports) != 0 {
		t.Fatalf("a bare container port is not published: %v", c.Ports)
	}
}

func TestZeroApprovedIsInvalid(t *testing.T) {
	if (Approved{}).Valid() {
		t.Fatal("the zero Approved must not be valid")
	}
}
