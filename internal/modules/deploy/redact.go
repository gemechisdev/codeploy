package deploy

import (
	"sort"
	"strings"
)

// MinSecretLen is the shortest secret value that is redacted. Shorter values would mangle ordinary log text
// (a secret of "a" would blank every "a"), so the policy is: the minimum is enforced, and the editor tells the
// user that very short secrets are not masked in logs.
const MinSecretLen = 6

// Redactor replaces exact secret values with *** in build and runtime logs. Longer values are replaced first so a
// secret that contains another is fully masked.
type Redactor struct{ vals []string }

func NewRedactor(secrets []string) *Redactor {
	seen := map[string]bool{}
	var v []string
	for _, s := range secrets {
		if len(s) >= MinSecretLen && !seen[s] {
			seen[s] = true
			v = append(v, s)
		}
	}
	sort.Slice(v, func(i, j int) bool { return len(v[i]) > len(v[j]) })
	return &Redactor{vals: v}
}

// Redact masks every secret occurrence in line. A nil Redactor returns the line unchanged.
func (r *Redactor) Redact(line string) string {
	if r == nil {
		return line
	}
	for _, v := range r.vals {
		if strings.Contains(line, v) {
			line = strings.ReplaceAll(line, v, "***")
		}
	}
	return line
}
