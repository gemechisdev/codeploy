package policy

import "testing"

func TestEnforceRejectsDangerousContainers(t *testing.T) {
	base := Options{ResourceID: "res_1", VolumesRoot: "/var/lib/codeplo/volumes"}
	okImg := Container{Image: "nginx:alpine", Network: "env_prod"}

	cases := []struct {
		name string
		mut  func(Container) Container
		ctl  string
	}{
		{"privileged", func(c Container) Container { c.Privileged = true; return c }, CtlPrivileged},
		{"host network", func(c Container) Container { c.NetworkMode = "host"; return c }, CtlNamespaces},
		{"docker.sock", func(c Container) Container {
			c.Mounts = []Mount{{Type: "bind", Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"}}
			return c
		}, CtlDockerSock},
		{"forbidden bind", func(c Container) Container {
			c.Mounts = []Mount{{Type: "bind", Source: "/etc/passwd", Target: "/etc/passwd"}}
			return c
		}, CtlBindMounts},
		{"extra caps", func(c Container) Container { c.CapAdd = []string{"SYS_ADMIN"}; return c }, CtlCapDrop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Enforce(tc.mut(okImg), base)
			if res.OK() || res.Approved.Valid() {
				t.Fatalf("must not approve %s: %#v", tc.name, res)
			}
			found := false
			for _, v := range res.Violations {
				if v.Control == tc.ctl {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected control %s in %#v", tc.ctl, res.Violations)
			}
		})
	}
	// happy path mints Approved
	res := Enforce(okImg, base)
	if !res.OK() || !res.Approved.Valid() {
		t.Fatalf("safe container must be approved: %#v", res)
	}
}
