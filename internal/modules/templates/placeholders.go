package templates

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// kv is one mapping entry with its value node. Order is kept because the catalog shows a template's variables in
// the order the template writes them.
type kv struct {
	key string
	val *yaml.Node
}

// entries returns a mapping node's pairs in document order.
func entries(n *yaml.Node) []kv {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]kv, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, kv{n.Content[i].Value, n.Content[i+1]})
	}
	return out
}

// child looks one key up in a mapping node.
func child(n *yaml.Node, key string) *yaml.Node {
	for _, e := range entries(n) {
		if e.key == key {
			return e.val
		}
	}
	return nil
}

// placeholderRe matches a "${...}" placeholder and, separately, a "$$" escape. Matching the escape first is what
// keeps a compose file's own "$$literal" out of the template's variable list.
var placeholderRe = regexp.MustCompile(`\$\$|\$\{[^{}]*\}`)

// magicPrefixes are the placeholder names this panel fills itself. SERVICE_* are the names the Coolify catalog
// gives its generated values, so an imported template arrives with its secrets already pointing at the panel.
var magicPrefixes = []struct {
	prefix string
	fill   Fill
}{
	{"CODEPLO_PASSWORD_", FillPassword},
	{"CODEPLO_PASSWORD", FillPassword},
	{"CODEPLO_NAME", FillName},
	{"CODEPLO_URL", FillURL},
	{"CODEPLO_FQDN", FillURL},
	{"SERVICE_PASSWORD_", FillPassword},
	{"SERVICE_PASSWORD", FillPassword},
	{"SERVICE_DB_PASSWORD_", FillPassword},
	{"SERVICE_USER_PASSWORD_", FillPassword},
	{"SERVICE_FQDN", FillURL},
	{"SERVICE_BASE_URL", FillURL},
	{"SERVICE_URL", FillURL},
}

// readPlaceholders collects every ${...} in the body and decides what the panel does with each. A placeholder the
// panel cannot answer is a refusal, not an empty string in a container's environment.
func (t *Template) readPlaceholders(root *yaml.Node) error {
	var found []string
	scanPlaceholders(root, &found)
	seen := map[string]bool{}
	for _, inner := range found {
		v, err := classify(inner)
		if err != nil {
			return err
		}
		if seen[v.Name] {
			continue
		}
		seen[v.Name] = true
		t.Variables = append(t.Variables, v)
	}
	return nil
}

// scanPlaceholders walks the document in order and gathers the inside of every placeholder, escapes excluded.
func scanPlaceholders(n *yaml.Node, out *[]string) {
	if n == nil {
		return
	}
	if n.Value != "" {
		*out = append(*out, placeholdersIn(n.Value)...)
	}
	for _, c := range n.Content {
		scanPlaceholders(c, out)
	}
}

// placeholdersIn returns each "${...}" body in s, in order. A "$" that is neither an escape nor a whole
// placeholder is a template that does not know what it wants, and saying so beats shipping half of one.
func placeholdersIn(s string) []string {
	var out []string
	for _, m := range placeholderRe.FindAllString(s, -1) {
		if m == "$$" {
			continue
		}
		out = append(out, m[2:len(m)-1])
	}
	if left := strings.Count(strings.ReplaceAll(s, "$$", ""), "${"); left != len(out) {
		out = append(out, "!unclosed placeholder")
	}
	return out
}

// classify turns the inside of a placeholder into what the panel will do with it. The forms are the Compose ones:
// ${NAME}, ${NAME:-value} and ${NAME:?what it is for}.
func classify(inner string) (Variable, error) {
	name, desc, def := inner, "", ""
	hasDefault := false
	if i := strings.Index(inner, ":?"); i >= 0 {
		name, desc = inner[:i], inner[i+2:]
	} else if i := strings.Index(inner, ":-"); i >= 0 {
		name, def, hasDefault = inner[:i], inner[i+2:], true
	}
	name = strings.TrimSpace(name)
	if name == "!unclosed placeholder" {
		return Variable{}, fmt.Errorf("the template has a ${ that is never closed")
	}
	if !varNameRe.MatchString(name) {
		return Variable{}, fmt.Errorf("${%s} is not a variable name", inner)
	}

	v := Variable{Name: name, Description: strings.TrimSpace(desc)}
	if fill, length := magic(name); fill != FillNone {
		if hasDefault {
			return Variable{}, fmt.Errorf("%s is filled by the panel, so the template may not also give it a value (%s)", name, def)
		}
		v.Fill, v.Secret, v.FillLen = fill, fill == FillPassword, length
		return v, nil
	}
	if !hasDefault {
		if secretNameRe.MatchString(name) {
			return Variable{}, fmt.Errorf("%s is a credential and the template leaves it empty: write ${CODEPLO_PASSWORD} and the panel will generate it", name)
		}
		return Variable{}, fmt.Errorf("%s has no value: the template gives it no default and the panel does not fill that name", name)
	}
	if secretNameRe.MatchString(name) {
		return Variable{}, fmt.Errorf("%s is a credential with a value written into the template (%s): a catalog entry that ships a password is not deployable, so write ${CODEPLO_PASSWORD} instead", name, def)
	}
	v.Default, v.HasDefault = def, true
	if v.Description == "" {
		v.Description = "Default: " + def
	}
	return v, nil
}

// magic reports how the panel fills a placeholder name it owns, and with what length when it generates one.
func magic(name string) (Fill, int) {
	for _, m := range magicPrefixes {
		if !strings.HasPrefix(name, m.prefix) {
			continue
		}
		if m.fill != FillPassword {
			return m.fill, 0
		}
		suffix := strings.TrimPrefix(name, m.prefix)
		if suffix == "" {
			return FillPassword, defaultPasswordLen
		}
		if suffix[0] != '_' {
			continue
		}
		n, err := strconv.Atoi(suffix[1:])
		if err != nil || n < minPasswordLen || n > maxPasswordLen {
			continue
		}
		return FillPassword, n
	}
	return FillNone, 0
}
