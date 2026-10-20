// Package hardening: P2a audit engine. Audit mode is strictly read-only: detectors only read files
// under Root and never exec, write, or touch the network. (Plan 7 / P2a business rules.)
package hardening

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Status string

const (
	Pass    Status = "pass"
	Warn    Status = "warn"
	Fail    Status = "fail"
	Skipped Status = "skipped"
)

type PlanDiff struct {
	Target string `json:"target"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Check mirrors the OpenAPI HardeningCheck object.
type Check struct {
	Key          string    `json:"key"`
	Tier         int       `json:"tier"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Status       Status    `json:"status"`
	Driver       string    `json:"driver"`
	DetectDetail string    `json:"detect_detail"`
	PlanDiff     *PlanDiff `json:"plan_diff"`
	ApplyEnabled bool      `json:"apply_enabled"`
	RequiresAck  bool      `json:"requires_ack,omitempty"`
	Experimental bool      `json:"experimental,omitempty"`
	ApplyFlag    *string   `json:"apply_flag,omitempty"`
}

type def struct {
	key, name, desc, driver string
	tier                    int
	detect                  func(Env) (Status, string)
}

// Env gives detectors read-only access to the host; Root lets tests point at a fixture tree.
type Env struct{ Root string }

func (e Env) path(p string) string { return filepath.Join(e.Root, filepath.FromSlash(p)) }
func (e Env) read(p string) (string, bool) {
	b, err := os.ReadFile(e.path(p))
	return string(b), err == nil
}
func (e Env) exists(p string) bool { _, err := os.Stat(e.path(p)); return err == nil }

// Defs are the baseline checks. Tier 0 report-only, Tier 1 safe fixes (apply lands later in P2a), Tier 2/3 plan-only.
var defs = []def{
	{"upd.unattended", "Automatic security updates", "Unattended security upgrades are enabled.", "apt", 1, func(e Env) (Status, string) {
		s, ok := e.read("etc/apt/apt.conf.d/20auto-upgrades")
		if !ok {
			return Fail, "20auto-upgrades not present"
		}
		if strings.Contains(s, `Unattended-Upgrade "1"`) {
			return Pass, "unattended-upgrades enabled"
		}
		return Fail, "unattended-upgrades not enabled"
	}},
	{"kern.sysctl", "Kernel network hardening", "rp_filter, syncookies and redirect settings.", "sysctl", 1, func(e Env) (Status, string) {
		want := map[string]string{"proc/sys/net/ipv4/conf/all/rp_filter": "1", "proc/sys/net/ipv4/tcp_syncookies": "1", "proc/sys/net/ipv4/conf/all/accept_redirects": "0"}
		var bad []string
		for p, v := range want {
			if got, ok := e.read(p); !ok {
				return Skipped, "sysctl not readable on this host"
			} else if strings.TrimSpace(got) != v {
				bad = append(bad, filepath.Base(p)+"="+strings.TrimSpace(got))
			}
		}
		if len(bad) > 0 {
			return Fail, "non-hardened: " + strings.Join(bad, ", ")
		}
		return Pass, "network sysctls hardened"
	}},
	{"bf.protection", "Brute-force protection", "fail2ban or CrowdSec is active.", "fail2ban", 1, func(e Env) (Status, string) {
		if e.exists("run/fail2ban/fail2ban.pid") || e.exists("run/crowdsec.pid") {
			return Pass, "protection daemon running"
		}
		return Fail, "no fail2ban/CrowdSec pid found"
	}},
	{"acct.operator", "Operator identity", "A usable non-root sudo operator exists.", "users", 1, func(e Env) (Status, string) {
		pw, ok := e.read("etc/passwd")
		if !ok {
			return Skipped, "passwd not readable"
		}
		grp, _ := e.read("etc/group")
		var sudoers string
		for _, l := range strings.Split(grp, "\n") {
			f := strings.Split(l, ":")
			if len(f) == 4 && (f[0] == "sudo" || f[0] == "wheel") {
				sudoers += "," + f[3]
			}
		}
		for _, l := range strings.Split(pw, "\n") {
			f := strings.Split(l, ":")
			if len(f) >= 7 && f[0] != "root" && !strings.HasSuffix(f[6], "nologin") && !strings.HasSuffix(f[6], "false") &&
				strings.Contains(","+sudoers+",", ","+f[0]+",") {
				return Pass, "sudo operator candidate: " + f[0]
			}
		}
		return Fail, "no sudo user with a login shell"
	}},
	{"net.exposed_services", "Exposed services", "Listeners on public interfaces.", "network", 0, func(e Env) (Status, string) {
		s, ok := e.read("proc/net/tcp")
		if !ok {
			return Skipped, "/proc/net/tcp not readable"
		}
		n := 0
		for _, l := range strings.Split(s, "\n")[1:] {
			f := strings.Fields(l)
			if len(f) > 3 && f[3] == "0A" && !strings.HasPrefix(f[1], "0100007F:") {
				n++
			}
		}
		if n > 3 {
			return Warn, "public IPv4 listeners: " + itoa(n)
		}
		return Pass, "public IPv4 listeners: " + itoa(n)
	}},
	{"net.docker_published", "Docker published ports", "Published ports reachable externally.", "docker", 0, func(e Env) (Status, string) {
		if !e.exists("var/run/docker.sock") {
			return Skipped, "docker socket absent"
		}
		return Warn, "docker present; port inspection requires the runtime module (P4)"
	}},
	{"fw.default_deny", "Default-deny firewall", "Inbound policy is default-deny (plan-only until P2b).", "nftables", 2, fwDetect},
	{"ssh.no_password", "SSH password login disabled", "Effective PasswordAuthentication is no (plan-only).", "sshd", 3, sshDetect("passwordauthentication", "no")},
	{"ssh.no_root_password", "SSH root password login", "PermitRootLogin prohibit-password or stricter (plan-only).", "sshd", 3, sshDetect("permitrootlogin", "prohibit-password", "no", "without-password")},
	{"ssh.limit_auth", "SSH auth limits", "MaxAuthTries <= 4 and no empty passwords (plan-only).", "sshd", 3, sshDetect("permitemptypasswords", "no")},
}

func itoa(n int) string { return strconv.Itoa(n) }

// fwDetect reads persisted firewall configuration only (audit mode never execs). It recognises a policy the
// operator wrote themselves and codeplo's own table, which the boot conf pulls in with a single include line.
// A persisted-but-unloaded table reports Warn: proving it is live needs `nft list`, which audit mode cannot run.
func fwDetect(e Env) (Status, string) {
	conf, ok := e.read(strings.TrimPrefix(NftBootConfPath, "/"))
	if !ok {
		return Fail, "no default-deny policy found"
	}
	if strings.Contains(conf, "policy drop") {
		return Pass, "nftables policy drop configured"
	}
	if strings.Contains(conf, NftIncludeLine) {
		table, has := e.read(strings.TrimPrefix(NftBootTablePath, "/"))
		if has && strings.Contains(table, "policy drop") {
			return Warn, "table inet " + NftTableName + " is configured for boot; verify it is live with nft list"
		}
		return Fail, "the boot conf includes " + NftBootTablePath + " but that table body is missing"
	}
	return Fail, "no default-deny policy found"
}

// sshDetect reads sshd_config and drop-ins (static text only; `sshd -T` effective checks need exec and arrive with preflight).
func sshDetect(key string, ok ...string) func(Env) (Status, string) {
	return func(e Env) (Status, string) {
		files := []string{"etc/ssh/sshd_config"}
		if ents, err := os.ReadDir(e.path("etc/ssh/sshd_config.d")); err == nil {
			for _, d := range ents {
				files = append([]string{"etc/ssh/sshd_config.d/" + d.Name()}, files...) // first match wins; drop-ins sort first
			}
		}
		for _, f := range files {
			s, found := e.read(f)
			if !found {
				continue
			}
			for _, l := range strings.Split(s, "\n") {
				p := strings.Fields(strings.TrimSpace(l))
				if len(p) >= 2 && strings.EqualFold(p[0], key) {
					for _, v := range ok {
						if strings.EqualFold(p[1], v) {
							return Pass, key + " " + p[1] + " (" + f + ")"
						}
					}
					return Fail, key + " " + p[1] + " (" + f + ")"
				}
			}
		}
		return Warn, key + " not set explicitly (default applies; verify with sshd -T)"
	}
}

// Run executes every detector. It never mutates anything.
func Run(e Env, applyFlags map[string]bool) []Check {
	out := make([]Check, 0, len(defs))
	for _, d := range defs {
		st, detail := d.detect(e)
		c := Check{Key: d.key, Tier: d.tier, Name: d.name, Description: d.desc, Status: st, Driver: d.driver, DetectDetail: detail}
		switch d.tier {
		case 2:
			f := "hardening.apply_tier2"
			c.ApplyFlag, c.ApplyEnabled, c.RequiresAck = &f, false, false
		case 3:
			f := "hardening.apply_tier3"
			c.ApplyFlag, c.RequiresAck, c.Experimental = &f, true, true
		}
		if d.tier >= 2 {
			c.ApplyEnabled = applyFlags[*c.ApplyFlag]
		}
		out = append(out, c)
	}
	return out
}

// Score is the percentage of scored checks (pass counts 1, warn 0.5; skipped excluded).
func Score(cs []Check) int {
	var got, total float64
	for _, c := range cs {
		switch c.Status {
		case Pass:
			got++
			total++
		case Warn:
			got += 0.5
			total++
		case Fail:
			total++
		}
	}
	if total == 0 {
		return 0
	}
	return int(got * 100 / total)
}
