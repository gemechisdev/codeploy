package templates

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	bundled "github.com/codeplo/codeplo/templates"
)

// Issue is one file the catalog could not list, and the sentence that says why. Issues are never shown in the
// panel, because there is no contract field for them; the daemon logs them at boot and `codeplo templates check`
// prints them, which is where an operator who just dropped a file in is looking.
type Issue struct {
	File   string
	Reason string
}

func (i Issue) Error() string { return i.File + ": " + i.Reason }

// NotFound is the catalog's answer to a key it does not have.
type NotFound struct{ Key string }

func (e NotFound) Error() string { return "no template named " + e.Key }

// Catalog is a template source: the embedded bundle, plus an operator's drop-in directory.
//
// It reads its files on every call rather than caching them, because a template is added by dropping a file into
// the data directory and a panel that needs a restart to notice has already answered "not found" to the operator
// who did. The catalog is a handful of small files read on a page load, which is not a thing to cache.
type Catalog struct {
	// Dir is the operator's drop-in directory (normally <DataDir>/templates). Empty or missing means the bundled
	// catalog only.
	Dir string
	// Bundled replaces the embedded catalog. Tests use it; nil means the shipped files.
	Bundled fs.FS
}

// Service is the catalog the daemon wires into the API. Deploying a template is what needs the database and the
// master key; listing it needs neither.
type Service struct {
	Catalog
	DB  *sql.DB
	Key []byte
}

func New() *Service { return &Service{Catalog: Catalog{}} }

// List returns every deployable template, grouped for reading: by category, then by name.
func (c *Catalog) List() ([]Template, []Issue) {
	files, issues := c.read()
	out := make([]Template, 0, len(files))
	for _, f := range files {
		t, err := Parse([]byte(f.content), f.key, f.source)
		if err != nil {
			issues = append(issues, Issue{File: f.source, Reason: err.Error()})
			continue
		}
		out = append(out, *t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out, issues
}

// Get looks one template up by key.
func (c *Catalog) Get(key string) (*Template, error) {
	files, _ := c.read()
	for _, f := range files {
		if f.key != key {
			continue
		}
		t, err := Parse([]byte(f.content), f.key, f.source)
		if err != nil {
			return nil, err
		}
		return t, nil
	}
	return nil, NotFound{Key: key}
}

type catalogFile struct {
	key, source, content string
}

// read gathers the catalog files: the embedded ones first, then the operator's, where a file with a key the bundle
// already uses replaces it. Replacing rather than refusing is what lets an operator pin an image tag or fix a
// default without forking the panel.
func (c *Catalog) read() ([]catalogFile, []Issue) {
	byKey := map[string]catalogFile{}
	var order []string
	var issues []Issue
	add := func(key, source, content string) {
		if _, seen := byKey[key]; !seen {
			order = append(order, key)
		}
		byKey[key] = catalogFile{key, source, content}
	}

	root := c.Bundled
	if root == nil {
		root = bundled.Compose
	}
	base := bundled.Dir
	if c.Bundled != nil {
		base = "."
	}
	listed, err := fs.ReadDir(root, base)
	if err != nil {
		issues = append(issues, Issue{File: base, Reason: "the bundled catalog cannot be read: " + err.Error()})
	} else {
		for _, e := range listed {
			key, content, ok := templateFile(e, root, base)
			if !ok {
				continue
			}
			add(key, path.Join("builtin", e.Name()), content)
		}
	}

	if c.Dir != "" {
		disk, err := os.ReadDir(c.Dir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				issues = append(issues, Issue{File: c.Dir, Reason: "the template directory cannot be read: " + err.Error()})
			}
		}
		for _, e := range disk {
			key := templateKey(e.Name())
			if e.IsDir() || key == "" {
				continue
			}
			p := filepath.Join(c.Dir, e.Name())
			raw, err := os.ReadFile(p)
			if err != nil {
				issues = append(issues, Issue{File: p, Reason: "the file cannot be read: " + err.Error()})
				continue
			}
			add(key, p, string(raw))
		}
	}

	out := make([]catalogFile, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out, issues
}

// templateFile reads one directory entry from an fs.FS, skipping anything that is not a readable YAML template.
func templateFile(e fs.DirEntry, root fs.FS, base string) (key, content string, ok bool) {
	key = templateKey(e.Name())
	if e.IsDir() || key == "" {
		return "", "", false
	}
	raw, err := fs.ReadFile(root, path.Join(base, e.Name()))
	if err != nil {
		return "", "", false
	}
	return key, string(raw), true
}

// templateKey is a file's catalog key: its name without the extension, or "" for a file that is not a template. A
// README in the template directory is documentation, not a broken entry.
func templateKey(name string) string {
	ext := yamlExt(name)
	if ext == "" {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(name), "."+ext)
}

// yamlExt returns a file's yaml extension, or "" for anything else, so a README sitting in the template directory
// is not read as a catalog entry.
func yamlExt(name string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(name), ".yaml"):
		return "yaml"
	case strings.HasSuffix(strings.ToLower(name), ".yml"):
		return "yml"
	}
	return ""
}

// Summary is the one line the panel and the CLI both show for a template.
func (t *Template) Summary() string {
	return fmt.Sprintf("%s (%s) — %s, %d MB, port %d", t.Name, t.Key, t.Kind, t.RecommendedMemoryMB, t.DefaultPort)
}
