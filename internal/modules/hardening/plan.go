package hardening

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/codeplo/codeplo/internal/platform"
)

var (
	ErrNotApplicable = errors.New("check has no apply action")
	ErrTierBlocked   = errors.New("tier 2/3 apply is plan-only until its VM matrix passes")
	ErrNeedsParam    = errors.New("a required parameter is missing")
)

// Cmd is one command executed without a shell.
type Cmd struct {
	Name string
	Args []string
}

func (c Cmd) String() string { return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " ")) }

// FileChange is one file the apply step writes (absolute path on the host, rooted at Env.Root for tests).
type FileChange struct {
	Path    string
	Content string
	Mode    os.FileMode
}

// Plan is the exact change set for one check.
type Plan struct {
	CheckKey string
	Tier     int
	Diff     *PlanDiff
	Pre      []Cmd
	Files    []FileChange
	Post     []Cmd
	Operator string // acct.operator: the account to record
}

// Commands returns what the operator copies and runs by hand: exactly one paste-ready command (see Script), or
// none when the check has nothing to run. It used to return pseudo-lines such as "write /etc/x", which no shell can
// execute.
func (p Plan) Commands() []string {
	if s := p.Script(); s != "" {
		return []string{s}
	}
	return []string{}
}

// Params are the per-request inputs to planning.
type Params struct {
	AdminIPs []string // current admin source IPs (fail2ban ignore list)
	Operator string   // operator account (setup_state.operator or first sudo candidate)
}

// Planner builds plans, preflights and applies Tier 1 changes. Run is injectable for tests.
type Planner struct {
	Env Env
	Run platform.Runner
}

func tierOf(key string) (int, bool) {
	for _, d := range defs {
		if d.key == key {
			return d.tier, true
		}
	}
	return 0, false
}

// IgnoreList always contains loopback plus every admin IP so brute-force tooling cannot ban the operator.
func IgnoreList(admin []string) []string {
	out := []string{"127.0.0.1/8", "::1"}
	seen := map[string]bool{}
	for _, a := range admin {
		ip := net.ParseIP(strings.TrimSpace(a))
		if ip == nil || ip.IsLoopback() || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		out = append(out, ip.String())
	}
	return out
}

func (p *Planner) before(path string) string {
	b, _ := p.Env.read(strings.TrimPrefix(path, "/"))
	return b
}

var portRe = regexp.MustCompile(`(?im)^\s*port\s+(\d{1,5})\s*$`)

// SSHPorts returns every port sshd listens on: effective `sshd -T` when available, else sshd_config text, else 22.
func (p *Planner) SSHPorts(ctx context.Context) []int {
	collect := func(s string) []int {
		set := map[int]bool{}
		for _, m := range portRe.FindAllStringSubmatch(s, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n < 65536 {
				set[n] = true
			}
		}
		var out []int
		for n := range set {
			out = append(out, n)
		}
		sort.Ints(out)
		return out
	}
	if p.Run != nil {
		if out, err := p.Run(ctx, "sshd", "-T"); err == nil {
			if ps := collect(out); len(ps) > 0 {
				return ps
			}
		}
	}
	files := []string{"etc/ssh/sshd_config"}
	if ents, err := os.ReadDir(p.Env.path("etc/ssh/sshd_config.d")); err == nil {
		for _, d := range ents {
			files = append(files, "etc/ssh/sshd_config.d/"+d.Name())
		}
	}
	for _, f := range files {
		if s, ok := p.Env.read(f); ok {
			if ps := collect(s); len(ps) > 0 {
				return ps
			}
		}
	}
	return []int{22}
}

const sshDropin = "/etc/ssh/sshd_config.d/00-codeplo.conf" // 00: sshd uses the FIRST value it reads

func sshBody(key string) string {
	switch key {
	case "ssh.no_password":
		return "PasswordAuthentication no\n"
	case "ssh.no_root_password":
		return "PermitRootLogin prohibit-password\n"
	default:
		return "MaxAuthTries 3\nPermitEmptyPasswords no\n"
	}
}

// NftRuleset renders the dedicated `table inet codeplo`. It is the table body both the runtime ruleset
// (NftRuntimeRuleset) and the persisted boot file (NftBootRuleset) are built from; the file that nft loads at
// apply time destroys only this table first, and nothing here ever flushes or edits another table.
func NftRuleset(ports []int) string {
	var ps []string
	for _, n := range ports {
		ps = append(ps, strconv.Itoa(n))
	}
	return "table inet codeplo {\n  chain input {\n    type filter hook input priority 0; policy drop;\n    ct state established,related accept\n    iif \"lo\" accept\n" +
		"    ip protocol icmp accept\n    ip6 nexthdr ipv6-icmp accept\n    tcp dport { " + strings.Join(ps, ", ") + " } accept\n  }\n}\n"
}

// withDriver attaches a driver's real bodies and argv to the plan and shows the first file as the plan diff.
func (p *Planner) withDriver(pl Plan, d Driver) Plan {
	pl.Pre, pl.Files, pl.Post = d.Pre, d.Files, d.Post
	if len(d.Files) > 0 {
		f := d.Files[0]
		pl.Diff = &PlanDiff{Target: f.Path, Before: p.before(f.Path), After: f.Content}
	}
	return pl
}

// Plan builds the change set. Tier 2/3 plans are generated for manual use but can never be applied here.
func (p *Planner) Plan(ctx context.Context, key string, prm Params) (Plan, error) {
	tier, ok := tierOf(key)
	if !ok {
		return Plan{}, fmt.Errorf("unknown check %q", key)
	}
	pl := Plan{CheckKey: key, Tier: tier}
	switch key {
	case "upd.unattended":
		return p.withDriver(pl, UnattendedUpgrades()), nil
	case "kern.sysctl":
		return p.withDriver(pl, Sysctl()), nil
	case "bf.protection":
		return p.withDriver(pl, Fail2ban(prm.AdminIPs)), nil
	case "acct.operator":
		if !platform.UsernameRe.MatchString(prm.Operator) {
			return pl, fmt.Errorf("%w: operator", ErrNeedsParam)
		}
		pl.Operator = prm.Operator
		pl.Diff = &PlanDiff{Target: "operator identity", Before: "none selected", After: prm.Operator}
	case "fw.default_deny":
		ports := NftAllowedPorts(p.SSHPorts(ctx))
		pl = p.withDriver(pl, Nftables(ports))
		pl.Diff = &PlanDiff{Target: "nftables: table inet " + NftTableName, Before: p.before(NftRulesetPath), After: NftRuntimeRuleset(ports)}
	case "ssh.no_password", "ssh.no_root_password", "ssh.limit_auth":
		return p.withDriver(pl, SSHDropin(platform.DistroID(), key)), nil
	default: // tier 0: report only
		return pl, ErrNotApplicable
	}
	return pl, nil
}

// AdminIPs gathers the current admin source IPs: the request's own address (when not loopback) and the
// source addresses of logged-in SSH sessions (`who`).
func AdminIPs(ctx context.Context, run platform.Runner, reqIP string) []string {
	set := []string{}
	add := func(s string) {
		if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil && !ip.IsLoopback() {
			set = append(set, ip.String())
		}
	}
	add(reqIP)
	if run != nil {
		if out, err := run(ctx, "who"); err == nil {
			re := regexp.MustCompile(`\(([0-9a-fA-F:.]+)\)`)
			for _, m := range re.FindAllStringSubmatch(out, -1) {
				add(m[1])
			}
		}
	}
	return IgnoreList(set)[2:]
}
