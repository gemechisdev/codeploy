package hardening

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/codeplo/codeplo/internal/platform"
)

// PreflightItem mirrors the OpenAPI PreflightItem.
type PreflightItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func item(key, label string, ok bool, detail string) PreflightItem {
	return PreflightItem{Key: key, Label: label, OK: ok, Detail: detail}
}

var includeRe = regexp.MustCompile(`(?mi)^\s*Include\s+\S*sshd_config\.d`)

// Preflight evaluates the blocking preconditions for a check. It only reads state and runs read-only commands
// (`sshd -t`, `sshd -T`, `nft list ruleset`, `who`).
func (p *Planner) Preflight(ctx context.Context, key, operator string, admin []string) ([]PreflightItem, error) {
	tier, ok := tierOf(key)
	if !ok {
		return nil, fmt.Errorf("unknown check %q", key)
	}
	var out []PreflightItem
	switch tier {
	case 0:
		return nil, ErrNotApplicable
	case 1:
		distro := platform.DistroID()
		out = append(out, item("distro", "Debian/Ubuntu host", platform.SupportedDistro(distro) || p.Env.Root != "/", "distribution: "+distro))
		out = append(out, item("root", "Process can write system files", os.Geteuid() == 0 || p.Env.Root != "/",
			"apply writes under /etc and runs apt/systemctl; the unprivileged service user cannot (privileged helper is open work)"))
		if key == "bf.protection" {
			// The generated jail is checked against the ignore list before it is ever written: a jail missing an
			// admin IP would let the operator be locked out of the host.
			body := Fail2banJailBody(admin)
			miss := Fail2banMissingIPs(admin, body)
			detail := "ignore list: " + strings.Join(Fail2banIgnoreIPs(body), ", ")
			if len(miss) > 0 {
				detail = "jail would allow banning: " + strings.Join(miss, ", ")
			}
			out = append(out, item("admin_ips", "Generated jail ignores every admin IP", len(miss) == 0, detail))
		}
		if key == "acct.operator" {
			out = append(out, item("operator", "Operator account selected", platform.UsernameRe.MatchString(operator), "operator: "+operator))
		}
	case 2:
		sshPorts := p.SSHPorts(ctx)
		ports := NftAllowedPorts(sshPorts)
		d := Nftables(ports)
		ruleset := ""
		if len(d.Files) > 0 {
			ruleset = d.Files[0].Content
		}
		out = append(out, item("ssh_ports", "Every sshd listener port is allowed", len(sshPorts) > 0,
			fmt.Sprintf("sshd ports %v, allowed set %v", sshPorts, ports)))
		out = append(out, item("edge_ports", "80/443 allowed for the edge", hasPort(ports, 80) && hasPort(ports, 443),
			"the edge ports are in the rule set: "+portList(ports)))
		out = append(out, item("own_table", "Rules live in table inet codeplo", NftRulesetIsAdditive(ruleset),
			"the existing ruleset is never flushed; revert deletes only the codeplo table"))
		out = append(out, item("runtime_first", "Apply loads runtime state only", NftApplyIsRuntimeOnly(d.Post),
			"confirm writes "+NftBootTablePath+", adds the include line to "+NftBootConfPath+" and enables nftables"))
		out = append(out, item("ipv6", "Default-deny covers IPv6",
			strings.Contains(ruleset, "inet "+NftTableName) && strings.Contains(ruleset, "ip6 nexthdr ipv6-icmp"),
			"the inet-family input chain applies to both address families"))
		if p.Run != nil {
			o, err := p.Run(ctx, "nft", "list", "ruleset")
			out = append(out, item("nft", "nft reads the live ruleset", err == nil, nftExistingTables(o, err)))
		} else {
			out = append(out, item("nft", "nft reads the live ruleset", false, "no command runner"))
		}
	case 3:
		sshConf, _ := p.Env.read("etc/ssh/sshd_config")
		out = append(out, item("include", "sshd_config includes sshd_config.d", includeRe.MatchString(sshConf), "otherwise apply refuses and never edits the main file"))
		if p.Run != nil {
			o, err := p.Run(ctx, "sshd", "-t")
			out = append(out, item("sshd_t", "sshd -t passes", err == nil, strings.TrimSpace(o)))
		} else {
			out = append(out, item("sshd_t", "sshd -t passes", false, "no command runner"))
		}
		out = append(out, p.authKeys(operator))
		out = append(out, p.pubkeyEvidence(operator))
		out = append(out, item("dropin_order", "Drop-in is named 00-codeplo.conf", true, "sshd uses the first value it reads; cloud-init ships 50-cloud-init.conf"))
		out = append(out, p.effectiveSSHD(ctx, key))
		d := SSHDropin(platform.DistroID(), key)
		out = append(out, item("reload_only", "sshd is reloaded, never restarted", sshReloadOnly(d.Post), "post commands: "+cmdLine(d.Post)))
		out = append(out, item("console_ack", "Provider console access acknowledged", false, "recorded in the audit log at apply time"))
	}
	return out, nil
}

// effectiveSSHD checks the EFFECTIVE configuration (`sshd -T`), not the file text: a cloud-init drop-in read
// before ours, or a Match block, changes what sshd actually uses.
func (p *Planner) effectiveSSHD(ctx context.Context, key string) PreflightItem {
	const label = "sshd -T reports the settings this check owns"
	if p.Run == nil {
		return item("effective", label, false, "no command runner")
	}
	o, err := p.Run(ctx, "sshd", "-T")
	if err != nil {
		return item("effective", label, false, "sshd -T failed: "+strings.TrimSpace(o))
	}
	eff := SSHEffective(o)
	var missing []string
	for _, s := range sshExpect[key] {
		if _, ok := eff[s]; !ok {
			missing = append(missing, s)
		}
	}
	cur := make([]string, 0, len(sshExpect[key]))
	for _, s := range sshExpect[key] {
		if v, ok := eff[s]; ok {
			cur = append(cur, s+"="+v)
		}
	}
	return item("effective", label, len(missing) == 0, "effective now: "+strings.Join(cur, " "))
}

func hasPort(ports []int, want int) bool {
	for _, p := range ports {
		if p == want {
			return true
		}
	}
	return false
}

func cmdLine(cmds []Cmd) string {
	parts := make([]string, 0, len(cmds))
	for _, c := range cmds {
		parts = append(parts, c.String())
	}
	return strings.Join(parts, "; ")
}

var nftTableRe = regexp.MustCompile(`(?m)^table\s+(\S+)\s+(\S+)\s*\{`)

// nftExistingTables names the tables present in `nft list ruleset`: the firewall change adds one table of our
// own and leaves every listed table (filter, nat and the Docker chains) exactly as it found them.
func nftExistingTables(out string, err error) string {
	if err != nil {
		return "nft list ruleset failed: " + strings.TrimSpace(out)
	}
	var names []string
	seen := map[string]bool{}
	for _, m := range nftTableRe.FindAllStringSubmatch(out, -1) {
		n := m[1] + " " + m[2]
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "live ruleset defines no table; ours will be the only one"
	}
	return "kept untouched: " + strings.Join(names, ", ")
}

func OK(items []PreflightItem) bool {
	for _, i := range items {
		if !i.OK {
			return false
		}
	}
	return true
}

func (p *Planner) authKeys(user string) PreflightItem {
	if !platform.UsernameRe.MatchString(user) {
		return item("authorized_keys", "Operator has valid authorized_keys", false, "no operator selected")
	}
	home := "home/" + user
	if user == "root" {
		home = "root"
	}
	f := p.Env.path(home + "/.ssh/authorized_keys")
	st, err := os.Stat(f)
	if err != nil {
		return item("authorized_keys", "Operator has valid authorized_keys", false, "authorized_keys not readable for "+user)
	}
	bad := st.Mode().Perm()&0o022 != 0
	if d, err := os.Stat(filepath.Dir(f)); err == nil && d.Mode().Perm()&0o022 != 0 {
		bad = true
	}
	return item("authorized_keys", "Operator has valid authorized_keys", !bad && st.Size() > 0, "authorized_keys present; permissions "+st.Mode().Perm().String())
}

// pubkeyEvidence requires a recent `Accepted publickey for <user>` entry: key login must be proven first.
func (p *Planner) pubkeyEvidence(user string) PreflightItem {
	needle := "Accepted publickey for " + user + " "
	for _, f := range []string{"var/log/auth.log", "var/log/secure"} {
		b, err := os.ReadFile(p.Env.path(f))
		if err != nil {
			continue
		}
		if len(b) > 2<<20 {
			b = b[len(b)-2<<20:]
		}
		if strings.Contains(string(b), needle) {
			return item("pubkey_evidence", "Key login proven in the auth log", true, "found 'Accepted publickey' for "+user)
		}
	}
	return item("pubkey_evidence", "Key login proven in the auth log", false, "no 'Accepted publickey' entry for "+user+"; log in once with your key, then retry")
}
