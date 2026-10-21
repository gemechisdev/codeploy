package templates

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/policy"
)

// keyRe is the catalog key rule. The key becomes a resource's default name, so it takes the same characters a
// resource name does.
var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// varNameRe is the environment-variable name rule, the same one the environment editor enforces.
var varNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// secretNameRe marks a placeholder that holds a credential whatever the template calls it. A name matching it can
// only be filled by the panel: a credential with a value in a source file is a shipped credential.
var secretNameRe = regexp.MustCompile(`(?i)(PASSWD|PASSWORD|PASSPHRASE|SECRET|TOKEN|CREDENTIAL|SALT|PRIVATE_KEY|ACCESS_KEY|API_KEY|_KEY$)`)

// metaLine is one metadata line: "# port: 5432". Coolify's templates write their headers this way, so a file from
// that catalog arrives with its header already in the shape this parser reads.
var metaLine = regexp.MustCompile(`^#[ \t]*([a-zA-Z][a-zA-Z0-9_-]*)[ \t]*:[ \t]*(.*)$`)

const (
	defaultPasswordLen = 32
	minPasswordLen     = 8
	maxPasswordLen     = 128
)

// Parse reads one template file. Every refusal here is a sentence an operator can act on, because a file that does
// not parse disappears from the panel and leaves only the reason behind.
func Parse(raw []byte, key, source string) (*Template, error) {
	if !keyRe.MatchString(key) {
		return nil, fmt.Errorf("%q is not a valid template key: lowercase letters, digits, %q and %q", key, "-", "_")
	}
	header, body := splitHeader(raw)
	meta := parseMeta(header)
	t := &Template{Key: key, Source: source, Compose: body, HealthPath: "/", Tags: []string{}}

	t.Description = meta["slogan"]
	if t.Description == "" {
		return nil, errors.New(`the header has no "# slogan:" line, and the catalog shows that sentence as the template's description`)
	}
	t.Category = strings.TrimSpace(meta["category"])
	if t.Category == "" {
		return nil, errors.New(`the header has no "# category:" line, which is how the catalog groups templates`)
	}
	t.Name, t.Documentation, t.Logo = meta["name"], meta["documentation"], meta["logo"]
	t.Tags = splitTags(meta["tags"])

	port, err := metaInt(meta, "port")
	if err != nil {
		return nil, err
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf(`"# port: %d" is not a port the panel can reach: use 1-65535`, port)
	}
	t.DefaultPort = port
	if mem, err := metaInt(meta, "memory"); err != nil {
		return nil, err
	} else if mem != 0 {
		t.RecommendedMemoryMB = mem
	}
	if h := strings.TrimSpace(meta["health"]); h != "" {
		t.HealthPath = h
	}
	if !strings.HasPrefix(t.HealthPath, "/") {
		return nil, fmt.Errorf(`"# health: %q" is not a path: the panel's observation asks for one`, t.HealthPath)
	}
	kind := Kind(strings.TrimSpace(meta["kind"]))
	if kind == "" {
		kind = KindApp
	} else if !kind.valid() {
		return nil, fmt.Errorf("%q is not a template kind (app, compose or database)", kind)
	}
	t.Kind = kind

	root, err := document(body)
	if err != nil {
		return nil, err
	}
	if err := t.readBody(root, body); err != nil {
		return nil, err
	}
	if t.Name == "" {
		t.Name = titleCase(t.Key)
	}
	if t.Icon = strings.TrimSpace(meta["icon"]); t.Icon == "" {
		t.Icon = defaultIcon(t.Kind)
	}
	if t.RecommendedMemoryMB == 0 {
		t.RecommendedMemoryMB = defaultMemoryMB(t.Kind)
	}
	if t.RecommendedMemoryMB < 16 {
		return nil, fmt.Errorf("# memory: %d is below the 16 MB the panel will not let a resource ask for", t.RecommendedMemoryMB)
	}
	if err := t.readPlaceholders(root); err != nil {
		return nil, err
	}
	return t, nil
}

// document parses a template body once, into the node tree the catalog reads structure and placeholders from. The
// node tree keeps mapping order, which is what makes a template's variable list come out the same on every load.
func document(body []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("the compose document is not valid YAML: %w", err)
	}
	if len(doc.Content) == 0 {
		return &yaml.Node{}, nil
	}
	return doc.Content[0], nil
}

// readBody takes the structure out of the compose document: the one container, its image, its volumes, and the
// verdict of the isolation policy.
func (t *Template) readBody(root *yaml.Node, body []byte) error {
	services := child(root, "services")
	svcs := entries(services)
	if len(svcs) == 0 {
		return errors.New("the template has no services")
	}
	if len(svcs) > 1 {
		names := make([]string, 0, len(svcs))
		for _, e := range svcs {
			names = append(names, e.key)
		}
		sort.Strings(names)
		return fmt.Errorf("this panel runs one container per resource and the template has %d services (%s): it needs the Compose runner, so it is not in the catalog",
			len(svcs), strings.Join(names, ", "))
	}
	t.Service = svcs[0].key
	// A resource this panel deploys runs the image's own command, is observed by the panel rather than by the
	// image's healthcheck, and has nothing to wait for. A template that relies on any of the three would deploy
	// into a container that is not the one the template describes, so it is refused here rather than half-run.
	for _, e := range entries(svcs[0].val) {
		switch e.key {
		case "command", "entrypoint":
			return fmt.Errorf("the template sets %s:, and this panel runs the image's own command: a resource cannot carry one", e.key)
		case "depends_on":
			return fmt.Errorf("the template sets depends_on: and has only the one service %q to start", t.Service)
		case "healthcheck":
			t.Warnings = append(t.Warnings, "the template's healthcheck is not used: the panel observes the resource through its own health path")
		}
	}

	containers, violations := policy.ParseCompose(body)
	if len(violations) > 0 {
		return errors.New(strings.Join(violationText(violations), "; "))
	}
	if len(containers) != 1 {
		return fmt.Errorf("the template service %q did not become exactly one container", t.Service)
	}
	c := containers[0]
	c.Network = "codeplo-template-check"
	res := policy.Enforce(c, policy.Options{
		ResourceID:    "template-" + t.Key,
		VolumesRoot:   "/var/lib/codeplo/volumes",
		DockerDataDir: "/var/lib/docker",
	})
	if !res.OK() {
		return errors.New(strings.Join(violationText(res.Violations), "; "))
	}
	for _, w := range res.Warnings {
		t.Warnings = append(t.Warnings, w.Message)
	}
	// The header's "# memory:" is the recommendation the catalog shows; a mem_limit in the body is the template's
	// own answer for the same question, and it only counts when the header did not give one.
	if t.RecommendedMemoryMB == 0 && c.MemoryBytes > 0 {
		t.RecommendedMemoryMB = int(c.MemoryBytes / (1 << 20))
	}
	t.Image = c.Image
	if strings.Contains(t.Image, "${") {
		return fmt.Errorf("the template picks its own image tag (%s); a catalog entry has to say which image it deploys", t.Image)
	}
	if !deploy.ValidImageRef(t.Image) {
		return fmt.Errorf("%q is not a valid image reference", t.Image)
	}
	for _, m := range c.Mounts {
		switch m.Type {
		case "volume":
			t.Volumes = append(t.Volumes, Volume{Name: m.Source, MountPath: m.Target})
		case "tmpfs":
		default:
			return fmt.Errorf("the template binds a host path (%s); the panel gives a template its data as a named volume, which it can move and delete with the resource", m.Source)
		}
	}
	for _, kv := range c.Env {
		k, v, _ := strings.Cut(kv, "=")
		if !varNameRe.MatchString(k) {
			return fmt.Errorf("%q is not a name the environment editor accepts", k)
		}
		// The placeholder rule refuses a credential the template leaves for a human. This one refuses a credential
		// the template answers itself, which is the same shipped secret either way.
		if secretNameRe.MatchString(k) && literalText(v) != "" {
			return fmt.Errorf("%s has a value written into the template (%s): a catalog entry that ships a credential is not deployable, so write ${CODEPLO_PASSWORD} and let the panel generate it", k, v)
		}
	}
	return nil
}

// literalText is what a value says that is not a placeholder: escapes unwrapped and every ${...} removed. It is how
// "hunter2${CODEPLO_PASSWORD}" still reads as a shipped secret rather than a generated one.
func literalText(v string) string {
	v = strings.ReplaceAll(v, "$$", "$")
	return strings.TrimSpace(placeholderRe.ReplaceAllString(v, ""))
}

// violationText flattens policy violations, naming the service only when there is more than one thing to name.
func violationText(vs []policy.Violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.String())
	}
	return out
}

func defaultIcon(k Kind) string {
	switch k {
	case KindDatabase:
		return "database"
	case KindCompose:
		return "layers"
	}
	return "box"
}

func defaultMemoryMB(k Kind) int {
	if k == KindDatabase {
		return 256
	}
	return 128
}

func splitTags(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitHeader peels the metadata lines off the front of a file. Metadata ends at the first line of real content, so
// a "# port: 8080" comment inside the body is a comment and not a second answer.
func splitHeader(raw []byte) (header []string, body []byte) {
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	i := 0
	for ; i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		if s == "" {
			continue
		}
		if !strings.HasPrefix(s, "#") {
			break
		}
		header = append(header, s)
	}
	rest := lines[i:]
	if len(rest) > 0 && strings.TrimSpace(rest[0]) == "---" {
		rest = rest[1:]
	}
	return header, []byte(strings.Join(rest, "\n"))
}

// parseMeta collects the header's key/value pairs. An unrecognised key is ignored rather than refused: imported
// catalogs carry fields this panel has no use for, and a template that is otherwise deployable is not worth
// dropping over a "# required_builders_note:".
func parseMeta(header []string) map[string]string {
	out := map[string]string{}
	for _, line := range header {
		m := metaLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		k := strings.ToLower(m[1])
		if _, seen := out[k]; !seen {
			out[k] = strings.TrimSpace(m[2])
		}
	}
	return out
}

func metaInt(meta map[string]string, key string) (int, error) {
	s := strings.TrimSpace(meta[key])
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf(`"# %s: %s" is not a number`, key, s)
	}
	return n, nil
}
