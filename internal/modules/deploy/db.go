package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
)

// EnvVar is one decrypted variable. Decrypted values exist only inside the engine (to build a container's
// environment and BuildKit secret mounts) and the redactor; nothing here is ever returned by the API.
type EnvVar struct {
	Key, Value, Scope string
	Secret            bool
}

func (v EnvVar) runtime() bool { return v.Scope == "runtime" || v.Scope == "both" }
func (v EnvVar) build() bool   { return v.Scope == "build" || v.Scope == "both" }

// LoadEnv decrypts a resource's variables.
func LoadEnv(ctx context.Context, db *sql.DB, key []byte, resourceID string) ([]EnvVar, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value_enc, scope, is_secret FROM resource_env_vars WHERE resource_id=? ORDER BY key`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvVar
	for rows.Next() {
		var v EnvVar
		var blob []byte
		var sec int
		if err := rows.Scan(&v.Key, &blob, &v.Scope, &sec); err != nil {
			return nil, err
		}
		pt, err := crypto.Open(key, blob)
		if err != nil {
			return nil, errors.New("could not decrypt variable " + v.Key + ": the master key does not match this database")
		}
		v.Value, v.Secret = string(pt), sec == 1
		out = append(out, v)
	}
	return out, rows.Err()
}

// SecretValues returns the values to redact from logs: every secret, whatever its scope. It is used by the
// deployment log writer and by the runtime log stream.
func SecretValues(ctx context.Context, db *sql.DB, key []byte, resourceID string) []string {
	vs, err := LoadEnv(ctx, db, key, resourceID)
	if err != nil {
		return nil
	}
	var out []string
	for _, v := range vs {
		if v.Secret {
			out = append(out, v.Value)
		}
	}
	return out
}

// Volume is one bind volume of a resource.
type Volume struct{ ID, Name, MountPath string }

func loadVolumes(ctx context.Context, db *sql.DB, resourceID string) ([]Volume, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, mount_path FROM volumes WHERE resource_id=? ORDER BY name`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Volume
	for rows.Next() {
		var v Volume
		if err := rows.Scan(&v.ID, &v.Name, &v.MountPath); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Resource is the engine's view of a resources row.
type Resource struct {
	ID, EnvID, EnvName, Type, Name, Slug, Desired string
	Cfg                                           ResourceConfig
	Active                                        string
}

func loadResource(ctx context.Context, db *sql.DB, id string) (Resource, error) {
	var r Resource
	var cfg string
	var active sql.NullString
	err := db.QueryRowContext(ctx, `SELECT r.id, r.environment_id, e.name, r.type, r.name, r.slug, r.config_json, r.desired_state, r.active_deployment_id
		FROM resources r JOIN environments e ON e.id=r.environment_id WHERE r.id=?`, id).
		Scan(&r.ID, &r.EnvID, &r.EnvName, &r.Type, &r.Name, &r.Slug, &cfg, &r.Desired, &active)
	if err != nil {
		return r, err
	}
	r.Active = active.String
	r.Cfg, err = ParseConfig(cfg)
	return r, err
}

// Dep is the engine's view of a deployments row.
type Dep struct {
	ID, ResourceID, Status, Step, Trigger, Mode string
	Branch, ImageRef, ImageDigest, ContainerID  string
	Upstream, PreviousID, RollbackOf            string
	CancelRequested                             bool
}

// ReusesImage reports whether the deployment starts from an image the host already retains instead of pulling or
// building. A rollback does; so does restarting a stopped resource whose image is still on the host, which is why
// the condition is the pointer to the source deployment and not the trigger.
func (d Dep) ReusesImage() bool { return d.RollbackOf != "" }

func loadDep(ctx context.Context, db *sql.DB, id string) (Dep, error) {
	var d Dep
	var br, dig, cid, prev, rb sql.NullString
	var cr int
	err := db.QueryRowContext(ctx, `SELECT id, resource_id, status, step, trigger, mode, branch, image_ref, image_digest, container_id, upstream, previous_deployment_id, rollback_of, cancel_requested
		FROM deployments WHERE id=?`, id).
		Scan(&d.ID, &d.ResourceID, &d.Status, &d.Step, &d.Trigger, &d.Mode, &br, &d.ImageRef, &dig, &cid, &d.Upstream, &prev, &rb, &cr)
	if err != nil {
		return d, err
	}
	d.Branch, d.ImageDigest, d.ContainerID, d.PreviousID, d.RollbackOf, d.CancelRequested = br.String, dig.String, cid.String, prev.String, rb.String, cr == 1
	return d, nil
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// credential decrypts one stored credential blob, or returns nil when none is stored.
func credential(ctx context.Context, db *sql.DB, key []byte, resourceID, kind string) []byte {
	var blob []byte
	if err := db.QueryRowContext(ctx, `SELECT blob_enc FROM resource_credentials WHERE resource_id=? AND kind=?`, resourceID, kind).Scan(&blob); err != nil {
		return nil
	}
	pt, err := crypto.Open(key, blob)
	if err != nil {
		return nil
	}
	return pt
}

type registryCred struct {
	Server   string `json:"server"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func registryFor(ctx context.Context, db *sql.DB, key []byte, resourceID string) *registryCred {
	raw := credential(ctx, db, key, resourceID, "registry")
	if raw == nil {
		return nil
	}
	var c registryCred
	if json.Unmarshal(raw, &c) != nil || c.Server == "" {
		return nil
	}
	return &c
}

func lower(s string) string { return strings.ToLower(s) }
