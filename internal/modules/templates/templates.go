// Package templates is the panel's one-click catalog (plan P7, J6): the Compose templates it ships, the ones an
// operator drops into its data directory, and what this panel can honestly do with either.
//
// Three rules shape it.
//
// A template is only listed when this panel can deploy it. The contract shows a card for every catalog entry and
// offers a Deploy button on each one, so an entry that cannot produce a running container is a UI lie; those files
// are reported as issues instead of being listed.
//
// Every credential comes from the panel. The deploy request carries no fields for template values, so a placeholder
// that needs a human answer is a template this panel cannot deploy, and a template with a password written into it
// would be a credential shipped in a binary. Generated secrets are sealed into the resource's environment and are
// write-only from there, like every other secret on the panel.
//
// A template body is checked by the isolation policy, not by a copy of its rules. An imported third-party template
// therefore meets the same "no privileged container, no Docker socket, no host paths" test as a hand-written
// compose file, with the same wording when it fails.
package templates

import "strings"

// Kind is what deploying a template creates. It is the contract's Template.kind and resources.type at once.
type Kind string

const (
	KindApp      Kind = "app"
	KindCompose  Kind = "compose"
	KindDatabase Kind = "database"
)

func (k Kind) valid() bool { return k == KindApp || k == KindCompose || k == KindDatabase }

// Fill is how the panel produces the value of a placeholder it owns.
type Fill string

const (
	// FillNone means the value came from the template's own default.
	FillNone Fill = ""
	// FillPassword is a generated credential: hex characters, so it needs no quoting anywhere a value lands.
	FillPassword Fill = "password"
	// FillName is the resource name the operator chose in the deploy request.
	FillName Fill = "name"
	// FillURL is the https address the panel will route to the resource, when it has one to route to.
	FillURL Fill = "url"
)

// Volume is a named volume a template asks for. The panel gives it a directory of its own, so a redeploy keeps the
// data and a delete takes it away with the resource.
type Volume struct {
	Name      string
	MountPath string
}

// Variable is one ${...} placeholder in a template body, and what the panel intends to do about it.
type Variable struct {
	Name string
	// Description is the text the template attached to the placeholder (`${X:?read this}`), which is the only
	// explanation an operator gets of what a generated value is for.
	Description string
	// Default is the template's own value for the placeholder, used when Fill is FillNone.
	Default    string
	HasDefault bool
	// Secret marks a credential: it is sealed at rest and never returned by any endpoint.
	Secret bool
	Fill   Fill
	// FillLen is the length of a generated password, in characters.
	FillLen int
}

// Template is one catalog entry: its metadata, and the single container it materialises into.
type Template struct {
	Key                 string
	Name                string
	Category            string
	Description         string
	Icon                string
	Kind                Kind
	DefaultPort         int
	RecommendedMemoryMB int
	HealthPath          string

	// Tags, Logo and Documentation come from the template header. They are what an operator reads before clicking
	// Deploy; none of them changes what gets created.
	Tags          []string
	Logo          string
	Documentation string

	// Service and Image are the one container this template becomes. Multi-service files are issues, not entries.
	Service string
	Image   string

	Variables []Variable
	Volumes   []Volume

	// Warnings never block a deploy but say what the template asked for that the panel does differently, such as a
	// Compose healthcheck the panel will answer with its own observation instead.
	Warnings []string

	// Source is "builtin" for an embedded file, otherwise the path it was read from.
	Source string
	// Compose is the template body without its metadata header.
	Compose []byte
}

// NeedsURL reports whether the template can only be deployed to an address, which is what makes a deploy without a
// domain refuse instead of producing a container configured with an empty URL.
func (t *Template) NeedsURL() bool {
	for _, v := range t.Variables {
		if v.Fill == FillURL {
			return true
		}
	}
	return false
}

// titleCase is the name a template gets when its header does not say one: "uptime-kuma" becomes "Uptime Kuma".
func titleCase(key string) string {
	parts := strings.FieldsFunc(key, func(r rune) bool { return r == '-' || r == '_' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		out = append(out, strings.ToUpper(p[:1])+p[1:])
	}
	if len(out) == 0 {
		return key
	}
	return strings.Join(out, " ")
}
