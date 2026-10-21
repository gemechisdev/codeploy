package git

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Connection is a panel-level git credential entry. Only 'deploy_key' is creatable in this build; 'github_app'
// is the Phase 7 identity and the CHECK on the column refuses anything else.
type Connection struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// Source is a resource's source of truth: where the image or the code comes from. Optional fields are pointers so
// the response can carry an explicit null, which is what the contract's oneOf string|null means.
type Source struct {
	Kind        string   `json:"kind"`
	RepoURL     *string  `json:"repo_url"`
	Branch      *string  `json:"branch"`
	BuildPath   *string  `json:"build_path"`
	Dockerfile  *string  `json:"dockerfile"`
	PathFilters []string `json:"path_filters"`
	DeployKeyID *string  `json:"deploy_key_id"`
}

// gitURLRe is the contract's repository rule: an SSH scp-style URL ending in .git, or an HTTPS URL with an
// optional .git suffix. Credentials embedded in a URL are refused because the regex has no place for a password,
// and builders.ValidateGitURL refuses them again at clone time.
var gitURLRe = regexp.MustCompile(`^(git@[\w.-]+:[\w./-]+\.git|https://[\w.-]+/[\w./-]+(\.git)?)$`)

// branchRe is deliberately narrower than git's own grammar: a branch name that starts with a dash would be read
// as an option by some plumbing commands, and control characters reach the log verbatim.
var branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// ValidBranch is the panel's branch rule, exported so a value arriving from outside the panel can be checked
// against the same grammar the source validator uses instead of a second copy of it.
func ValidBranch(name string) bool { return branchRe.MatchString(name) }

// ListConnections returns every connection. A panel holds a handful of them, so there is no cursor.
func (s *Service) ListConnections(ctx context.Context, q Querier) ([]Connection, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, kind, name, created_at FROM git_connections ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		var c Connection
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateConnection records a named connection. The name is what the operator recognises in the repository
// settings; the credential itself attaches to a resource, never to this row.
func (s *Service) CreateConnection(ctx context.Context, q Querier, name string) (Connection, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Connection{}, FieldErrors{"name": "Give the connection a name"}
	}
	c := Connection{ID: s.id("gcn_"), Kind: "deploy_key", Name: name, CreatedAt: s.ts()}
	_, err := q.ExecContext(ctx, `INSERT INTO git_connections(id,kind,name,created_at) VALUES (?,?,?,?)`, c.ID, c.Kind, c.Name, c.CreatedAt)
	if err != nil {
		return Connection{}, err
	}
	return c, nil
}

// DefaultSource is what a resource with no configured source is: an image pulled from a registry, which is how a
// resource created before any git wiring still deploys.
func DefaultSource() Source {
	return Source{Kind: KindRegistry, PathFilters: []string{}}
}

// GetSource reads a resource's source, or the default when it has none.
func (s *Service) GetSource(ctx context.Context, q Querier, resourceID string) (Source, error) {
	var (
		kind            string
		url, br, bp, df sql.NullString
		filters         string
		keyID           sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT kind, repo_url, branch, build_path, dockerfile, path_filters_json, deploy_key_id
		FROM resource_sources WHERE resource_id=?`, resourceID).
		Scan(&kind, &url, &br, &bp, &df, &filters, &keyID)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSource(), nil
	}
	if err != nil {
		return Source{}, err
	}
	src := Source{Kind: kind, RepoURL: str(url), Branch: str(br), BuildPath: str(bp), Dockerfile: str(df), DeployKeyID: str(keyID)}
	src.PathFilters = decodeFilters(filters)
	return src, nil
}

// decodeFilters reads path_filters_json defensively: a row written by an older build, or a hand-edited database,
// must not turn a source read into a 500.
func decodeFilters(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var v []string
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return []string{}
	}
	for _, p := range v {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ValidateSource applies the contract's rules and returns the normalised source. It checks the shape of the
// request, not whether the repository exists — that is what clone time is for.
func (s *Service) ValidateSource(ctx context.Context, q Querier, resourceID string, in Source) (Source, error) {
	if !validKind(in.Kind) {
		return Source{}, FieldErrors{"kind": "Choose a source kind"}
	}
	if in.Kind == KindRegistry {
		return DefaultSource(), nil
	}
	f := FieldErrors{}
	url := strings.TrimSpace(deref(in.RepoURL))
	if !gitURLRe.MatchString(url) {
		f["repo_url"] = "Enter an SSH (git@host:org/repo.git) or HTTPS repository URL"
	}
	br := strings.TrimSpace(deref(in.Branch))
	if br == "" {
		f["branch"] = "Branch is required"
	} else if !branchRe.MatchString(br) {
		f["branch"] = "Branch may contain letters, digits, dot, slash, underscore and dash"
	}
	// A build path or Dockerfile that escapes the clone is how a repository reads /etc into an image context. The
	// clone itself is confined again by builders.ConfinePath; this refuses the request before it is ever stored.
	df, badDf := cleanRel(deref(in.Dockerfile))
	if badDf {
		f["dockerfile"] = "Dockerfile must stay inside the build context"
	}
	bp, badBp := cleanRel(deref(in.BuildPath))
	if badBp {
		f["build_path"] = "Build path must stay inside the clone"
	}
	keyID := ""
	if in.Kind == KindDeployKey {
		if err := q.QueryRowContext(ctx, `SELECT id FROM deploy_keys WHERE resource_id=?`, resourceID).Scan(&keyID); errors.Is(err, sql.ErrNoRows) {
			f["deploy_key_id"] = "Generate a deploy key first"
		} else if err != nil {
			return Source{}, err
		}
	}
	if len(f) > 0 {
		return Source{}, f
	}
	src := Source{Kind: in.Kind, PathFilters: cleanFilters(in.PathFilters)}
	if url != "" {
		src.RepoURL = &url
	}
	if br != "" {
		src.Branch = &br
	}
	if df != "" {
		src.Dockerfile = &df
	}
	if bp != "" {
		src.BuildPath = &bp
	}
	if keyID != "" {
		src.DeployKeyID = &keyID
	}
	return src, nil
}

// relRe is the shape of a repository-relative path: no leading slash, no space, no shell punctuation. The ".."
// rule is checked separately because "a..b" is a legal filename while "a/../b" is not.
var relRe = regexp.MustCompile(`^[\w.][\w./-]{0,255}$`)

// cleanRel rejects an absolute or escaping relative path and returns the tidied value. An empty value is allowed:
// it means "the repository root", which is the default build context.
func cleanRel(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	if strings.HasPrefix(v, "/") || strings.HasPrefix(v, `\`) || strings.Contains(v, "..") || !relRe.MatchString(v) {
		return v, true
	}
	return strings.TrimSuffix(v, "/"), false
}

// cleanFilters drops blanks and duplicates so a stored source reads back exactly once per pattern.
func cleanFilters(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// PutSource stores the source and points the resource's own image config at it, because the engine reads
// config.image for the mode and the context while the panel reads resource_sources for everything else. Keeping
// the two in step here is what stops a UI that sets a Git source from still showing "pull from registry".
func (s *Service) PutSource(ctx context.Context, q Querier, resourceID string, src Source) error {
	filters, err := json.Marshal(src.PathFilters)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO resource_sources(resource_id,kind,repo_url,branch,build_path,dockerfile,path_filters_json,deploy_key_id)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(resource_id) DO UPDATE SET kind=excluded.kind, repo_url=excluded.repo_url, branch=excluded.branch,
			build_path=excluded.build_path, dockerfile=excluded.dockerfile, path_filters_json=excluded.path_filters_json,
			deploy_key_id=excluded.deploy_key_id`,
		resourceID, src.Kind, nullIfEmpty(src.RepoURL), nullIfEmpty(src.Branch), nullIfEmpty(src.BuildPath),
		nullIfEmpty(src.Dockerfile), string(filters), nullIfEmpty(src.DeployKeyID))
	if err != nil {
		return err
	}
	return s.syncImageConfig(ctx, q, resourceID, src)
}

func nullIfEmpty(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

// syncImageConfig rewrites config.image so the engine's mode matches the source kind. It patches the one subtree
// it owns and leaves every other config key exactly as stored, because a source change must not silently reset a
// port, a health check or an environment the operator set.
func (s *Service) syncImageConfig(ctx context.Context, q Querier, resourceID string, src Source) error {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT config_json FROM resources WHERE id=?`, resourceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return NotFound{What: "resource"}
	}
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return err
	}
	image, _ := cfg["image"].(map[string]any)
	if image == nil {
		image = map[string]any{}
	}
	mode := "build"
	if src.Kind == KindRegistry {
		mode = "pull"
	}
	image["mode"] = mode
	image["dockerfile"] = src.Dockerfile
	image["context"] = src.BuildPath
	cfg["image"] = image
	out, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE resources SET config_json=? WHERE id=?`, string(out), resourceID)
	return err
}
