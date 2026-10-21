package api

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/crypto"
)

// envKeyRe is the variable-name rule the UI enforces too (web/src/mock/util.ts).
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var volumeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,38}$`)

// forbiddenMounts cannot be mounted into a workload: each is the host itself, a kernel or runtime filesystem, or
// the directory another tenant's data lives in.
var forbiddenMounts = []string{"/", "/etc", "/proc", "/sys", "/dev", "/var/run", "/var/lib/docker"}

func (s *Server) envRoutes() []RouteDef {
	return []RouteDef{
		{Method: "GET", Path: "/resources/{id}/env", Perm: "env:read", Module: "deploy", Handler: s.listEnv},
		{Method: "PUT", Path: "/resources/{id}/env", Perm: "env:write", Module: "deploy", Handler: s.putEnv},
		{Method: "GET", Path: "/resources/{id}/volumes", Perm: "resource:read", Module: "deploy", Handler: s.listVolumes},
		{Method: "POST", Path: "/resources/{id}/volumes", Perm: "resource:write", Module: "deploy", Handler: s.createVolume},
	}
}

func (s *Server) listEnv(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	items, err := s.envItems(r, cur.ID)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

type envRow struct {
	id, key, value, scope, updatedAt string
	secret                           bool
}

// envItems reads a resource's variables with values decrypted. It exists for the write paths that need the old
// values to decide what changed; no route sends its output to a client for a secret row.
func (s *Server) envItems(r *http.Request, resourceID string) ([]map[string]any, error) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, key, value_enc, scope, is_secret, updated_at FROM resource_env_vars WHERE resource_id=? ORDER BY key`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var x envRow
		var blob []byte
		var sec int
		if err := rows.Scan(&x.id, &x.key, &blob, &x.scope, &sec, &x.updatedAt); err != nil {
			return nil, err
		}
		x.secret = sec == 1
		value := any("")
		if x.secret {
			value = nil // secrets are write-only: no endpoint here returns one, so nothing can leak one
		} else if pt, err := crypto.Open(s.Key, blob); err == nil {
			value = string(pt)
		} else {
			value = nil // a row this daemon cannot decrypt reads as empty, never as garbage
		}
		items = append(items, map[string]any{"id": x.id, "key": x.key, "value": value, "scope": x.scope, "is_secret": x.secret, "updated_at": x.updatedAt})
	}
	return items, rows.Err()
}

func (s *Server) previousEnv(ctx context.Context, resourceID string) (map[string]envRow, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, key, value_enc, scope, is_secret, updated_at FROM resource_env_vars WHERE resource_id=?`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]envRow{}
	for rows.Next() {
		var x envRow
		var blob []byte
		var sec int
		if err := rows.Scan(&x.id, &x.key, &blob, &x.scope, &sec, &x.updatedAt); err != nil {
			return nil, err
		}
		x.secret = sec == 1
		if pt, err := crypto.Open(s.Key, blob); err == nil {
			x.value = string(pt)
		}
		out[x.key] = x
	}
	return out, rows.Err()
}

// putEnv replaces a resource's variable set. It is a full replace because a partial one cannot express removal,
// and dropping a credential has to be an explicit act. Rotating a secret value requires step-up: a request that
// only holds a session cookie should not be able to swap a database password without a second factor.
func (s *Server) putEnv(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	p := from(r)
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var b struct {
		Items []struct {
			Key                string  `json:"key"`
			Value              *string `json:"value"`
			Scope              string  `json:"scope"`
			IsSecret           bool    `json:"is_secret"`
			KeepExistingSecret bool    `json:"keep_existing_secret"`
		} `json:"items"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Items == nil {
		invalid(w, map[string]string{"items": "items is required"})
		return
	}
	prev, err := s.previousEnv(r.Context(), cur.ID)
	if err != nil {
		internalErr(w, err)
		return
	}

	f := map[string]string{}
	seen := map[string]bool{}
	replacesSecret := false
	for i, it := range b.Items {
		kf := "items." + strconv.Itoa(i) + ".key"
		vf := "items." + strconv.Itoa(i) + ".value"
		if !envKeyRe.MatchString(it.Key) {
			f[kf] = `Keys must match [A-Za-z_][A-Za-z0-9_]*`
		} else if seen[it.Key] {
			f[kf] = "Duplicate key"
		}
		seen[it.Key] = true
		if it.Scope != "build" && it.Scope != "runtime" && it.Scope != "both" {
			f["items."+strconv.Itoa(i)+".scope"] = "Scope must be build, runtime or both"
		}
		old, had := prev[it.Key]
		if had && old.secret && !it.IsSecret {
			f[kf] = "A secret cannot be converted to a plain variable. Delete it and add a new one."
		}
		hasValue := it.Value != nil && *it.Value != ""
		if it.IsSecret {
			if !hasValue && !(had && old.secret && it.KeepExistingSecret) {
				if had && old.secret {
					f[vf] = "Enter a new value, or keep the existing secret"
				} else {
					f[vf] = "A secret needs a value"
				}
			}
			if hasValue && had && old.secret {
				replacesSecret = true
			}
		} else if it.Value == nil {
			f[vf] = "Value is required"
		}
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	if replacesSecret && !requireStepUp(w, p) {
		return
	}

	changed, removed := []string{}, []string{}
	stamp := now()
	err = s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		for _, it := range b.Items {
			old, had := prev[it.Key]
			value := ""
			if it.Value != nil {
				value = *it.Value
			} else if had && old.secret {
				value = old.value // keep_existing_secret: the row is rewritten unchanged
			}
			blob, err := crypto.Seal(s.Key, []byte(value))
			if err != nil {
				return audit.Event{}, err
			}
			secret := 0
			if it.IsSecret {
				secret = 1
			}
			updated := stamp
			if had && old.value == value && old.scope == it.Scope && old.secret == it.IsSecret {
				updated = old.updatedAt // an untouched row keeps its timestamp, so "changed" stays truthful
			} else {
				changed = append(changed, it.Key)
			}
			if had {
				if _, err := tx.ExecContext(r.Context(), `UPDATE resource_env_vars SET value_enc=?, scope=?, is_secret=?, updated_at=? WHERE resource_id=? AND key=?`,
					blob, it.Scope, secret, updated, cur.ID, it.Key); err != nil {
					return audit.Event{}, err
				}
				continue
			}
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO resource_env_vars(id,resource_id,key,value_enc,scope,is_secret,updated_at) VALUES (?,?,?,?,?,?,?)`,
				newID("env_"), cur.ID, it.Key, blob, it.Scope, secret, updated); err != nil {
				return audit.Event{}, err
			}
		}
		for k := range prev {
			if !seen[k] {
				if _, err := tx.ExecContext(r.Context(), `DELETE FROM resource_env_vars WHERE resource_id=? AND key=?`, cur.ID, k); err != nil {
					return audit.Event{}, err
				}
				removed = append(removed, k)
			}
		}
		// The audit row carries key names only. A value in an audit log would defeat encrypting it in the first place.
		return s.rec(r.Context(), tx, r, p, "env.updated", "resource", cur.ID,
			map[string]any{"changed": changed, "removed": removed, "secrets_replaced": replacesSecret})
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": cur.ID})
	items, err := s.envItems(r, cur.ID)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) listVolumes(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, name, mount_path, kind FROM volumes WHERE resource_id=? ORDER BY name`, cur.ID)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, mp, kind string
		if err := rows.Scan(&id, &name, &mp, &kind); err != nil {
			internalErr(w, err)
			return
		}
		items = append(items, map[string]any{
			"id": id, "name": name, "mount_path": mp, "host_path": e.VolumeHostPath(cur.Slug, name),
			"kind": kind, "size_mb": nil,
		})
	}
	if err := rows.Err(); err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) createVolume(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var b struct {
		Name      string `json:"name"`
		MountPath string `json:"mount_path"`
	}
	if !decode(w, r, &b) {
		return
	}
	f := map[string]string{}
	if !volumeNameRe.MatchString(b.Name) {
		f["name"] = `Use lowercase letters, digits, "-" and "_"`
	}
	mp := filepath.ToSlash(strings.TrimSpace(b.MountPath))
	clean := strings.TrimRight(mp, "/")
	if clean == "" {
		clean = "/"
	}
	if !strings.HasPrefix(mp, "/") || strings.Contains(mp, "..") {
		f["mount_path"] = "Mount path must be absolute"
	} else {
		for _, bad := range forbiddenMounts {
			if clean == bad {
				f["mount_path"] = mp + " is a protected path and cannot be mounted"
			}
		}
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	var vname, vpath string
	switch err := s.DB.QueryRowContext(r.Context(), `SELECT name, mount_path FROM volumes WHERE resource_id=? AND (name=? OR mount_path=?) LIMIT 1`, cur.ID, b.Name, mp).Scan(&vname, &vpath); err {
	case nil:
		if vname == b.Name {
			writeErr(w, 409, "conflict", `Volume "`+b.Name+`" already exists`, nil)
		} else {
			writeErr(w, 409, "conflict", "Something is already mounted at "+mp, nil)
		}
		return
	case sql.ErrNoRows:
	default:
		internalErr(w, err)
		return
	}
	id, created := newID("vol_"), now()
	// The directory is created with the row, not at deploy time: a volume that cannot be prepared should fail the
	// request that asked for it, not a deployment minutes later.
	if err := e.EnsureVolumeDir(cur.Slug, b.Name); err != nil {
		internalErr(w, err)
		return
	}
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO volumes(id,resource_id,name,mount_path,kind,created_at) VALUES (?,?,?,?,?,?)`,
			id, cur.ID, b.Name, mp, "bind", created); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "volume.created", "resource", cur.ID, map[string]any{"name": b.Name, "mount_path": mp})
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{
		"id": id, "name": b.Name, "mount_path": mp, "host_path": e.VolumeHostPath(cur.Slug, b.Name),
		"kind": "bind", "size_mb": nil,
	})
}
