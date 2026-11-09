package templates

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/modules/deploy"
)

// PlanInput is what a deploy adds to a template: the name the operator chose for the resource. It is all the
// contract's deploy request carries, which is why a placeholder needing anything else — an address, a value a human
// types — is refused when the template is parsed rather than resolved here.
type PlanInput struct {
	Name string
}

// EnvValue is one resolved environment entry, ready to seal. Secret values are write-only afterwards like every
// other secret on the panel, so nothing here keeps a copy of the plaintext beyond the plan.
type EnvValue struct {
	Key    string
	Value  string
	Secret bool
}

// Plan is a template reduced to what this panel creates: a resources row's config, its variables and its volumes.
type Plan struct {
	// Type is resources.type. A compose-kind template is a file format, not a workload: having survived the parser
	// it is exactly one container, so it becomes an app resource. resources.type=compose belongs to the multi-service
	// Compose runner this panel does not have yet, and putting a single container in that column would promise a
	// runner that nothing is running.
	Type       string
	Cfg        deploy.ResourceConfig
	ConfigJSON string
	Env        []EnvValue
	Volumes    []Volume
}

// Plan resolves every placeholder the template left for the panel.
func (t *Template) Plan(in PlanInput) (*Plan, error) {
	byName := map[string]Variable{}
	for _, v := range t.Variables {
		byName[v.Name] = v
	}
	resourceType := string(t.Kind)
	if t.Kind == KindCompose {
		resourceType = string(KindApp)
	}
	plan := &Plan{Type: resourceType, Volumes: t.Volumes}
	for _, e := range t.Env {
		k, raw, _ := strings.Cut(e, "=")
		value, secret, err := t.resolve(raw, byName, in)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		if len(value) > 4096 {
			return nil, fmt.Errorf("%s resolves to more than 4096 characters, which is not an environment value", k)
		}
		plan.Env = append(plan.Env, EnvValue{Key: k, Value: value, Secret: secret || secretNameRe.MatchString(k)})
	}

	ref := t.Image
	cfg := deploy.DefaultConfig()
	cfg.Image.Mode = "pull"
	cfg.Image.Ref = &ref
	cfg.Port = t.DefaultPort
	cfg.Healthcheck.Path = t.HealthPath
	cfg.Limits.MemoryMB = t.RecommendedMemoryMB
	if t.Kind == KindDatabase {
		// Two databases sharing one data directory is corruption, not a blue/green deploy: a template that owns
		// persistent state takes the rollout that never has two containers alive at once.
		cfg.Rollout = "stop_first"
		// Match official image non-root UIDs so entrypoints do not need setuid under no-new-privileges.
		u := "999"
		cfg.Isolation.User = &u
		cfg.Healthcheck.StartPeriodS = 20
		cfg.Healthcheck.IntervalS = 5
		cfg.Healthcheck.Retries = 18
	}
	plan.Cfg = cfg
	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	plan.ConfigJSON = string(out)
	return plan, nil
}

// resolve replaces every placeholder in one value. It reports whether any of them was a generated credential, which
// is what decides whether the row it lands in is write-only.
func (t *Template) resolve(raw string, byName map[string]Variable, in PlanInput) (string, bool, error) {
	secret, fail := false, error(nil)
	out := placeholderRe.ReplaceAllStringFunc(raw, func(m string) string {
		if fail != nil {
			return m
		}
		if m == "$$" {
			return "$"
		}
		name, _, _ := splitPlaceholder(m[2 : len(m)-1])
		v, ok := byName[name]
		if !ok {
			fail = fmt.Errorf("the template has no variable called %s", name)
			return ""
		}
		switch v.Fill {
		case FillPassword:
			secret = true
			return password(v.FillLen)
		case FillName:
			return in.Name
		}
		return v.Default
	})
	if fail != nil {
		return "", false, fail
	}
	if strings.ContainsRune(out, 0) {
		return "", false, fmt.Errorf("resolves to a value containing a NUL byte")
	}
	return out, secret, nil
}

// password is a generated credential of exactly n characters. Hex is used because it needs no quoting anywhere a
// value lands: a YAML file, a container environment, a connection string, or a dump tool's configuration.
func password(n int) string {
	if n < minPasswordLen {
		n = defaultPasswordLen
	}
	return crypto.RandomHex((n + 1) / 2)[:n]
}
