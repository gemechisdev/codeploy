package previews

import (
	"context"
	"database/sql"
	"errors"

	"github.com/codeplo/codeplo/internal/modules/git"
)

// previewColumns is the column list of a pr_previews row, and fields() below is its mirror in Go. The two are kept
// next to each other because a scan that silently read a column out of order would put a hostname in the sha field.
const previewColumns = `id, provider, parent_resource_id, preview_resource_id, number, head_ref, head_sha, hostname, created_at, updated_at, expires_at`

func (p *Preview) fields() []any {
	return []any{&p.ID, &p.Provider, &p.ParentResourceID, &p.ResourceID, &p.Number, &p.HeadRef, &p.HeadSHA,
		&p.Hostname, &p.CreatedAt, &p.UpdatedAt, &p.ExpiresAt}
}

// finish fills the URL from the hostname: the address is the name with a scheme in front of it, and storing both
// would be two ways to be wrong.
func (p *Preview) finish() Preview {
	c := *p
	c.URL = "https://" + c.Hostname
	return c
}

// readParent is the service a delivery resolved to, with the two things only its own rows can say: what it is (type,
// config) and what it builds from (source). The LEFT JOIN means a resource with no source row reads as an empty
// repository, which previewable refuses before anything tries to clone it.
func readParent(ctx context.Context, q git.Querier, id string) (parent, bool, error) {
	var p parent
	err := q.QueryRowContext(ctx, `SELECT r.id, r.environment_id, r.type, r.slug, r.config_json,
			COALESCE(s.kind,''), COALESCE(s.repo_url,''), COALESCE(s.build_path,''), COALESCE(s.dockerfile,'')
		FROM resources r LEFT JOIN resource_sources s ON s.resource_id=r.id WHERE r.id=?`, id).
		Scan(&p.id, &p.envID, &p.rtype, &p.slug, &p.configJSON, &p.kind, &p.repoURL, &p.buildPath, &p.dockerfile)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	return p, true, nil
}

// find is the lookup the refresh path is built on: one preview per (provider, service, pull request number).
func find(ctx context.Context, q git.Querier, parentID string, number int) (Preview, bool, error) {
	var p Preview
	err := q.QueryRowContext(ctx, `SELECT `+previewColumns+` FROM pr_previews WHERE provider='github' AND parent_resource_id=? AND number=?`,
		parentID, number).Scan(p.fields()...)
	if errors.Is(err, sql.ErrNoRows) {
		return Preview{}, false, nil
	}
	if err != nil {
		return Preview{}, false, err
	}
	return p.finish(), true, nil
}

// oldestPreviews returns the k previews of one service with the least reason to be alive: ordered by expiry, which
// is the panel's own measure of which branch nobody has pushed to in longest, with the id as the tiebreak so the
// order does not depend on two writes landing in the same second.
//
// A preview whose resource row is already gone sorts first, because closing one of those is a bookkeeping fix rather
// than a teardown and it should not have to wait behind live workloads.
func oldestPreviews(ctx context.Context, q git.Querier, parentID string, k int) ([]Preview, error) {
	if k <= 0 {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT `+previewColumns+` FROM pr_previews p
		WHERE p.provider='github' AND p.parent_resource_id=?
		ORDER BY (SELECT COUNT(*) FROM resources r WHERE r.id=p.preview_resource_id), p.expires_at, p.id LIMIT ?`, parentID, k)
	if err != nil {
		return nil, err
	}
	return scanPreviews(rows)
}

// duePreviews is the sweep's list: every preview whose own clock has run out, oldest first. The parent and resource
// questions are asked per row in the sweep's transaction, because an operator can delete either of them at any
// moment and the answer has to be current when the rows are removed.
func duePreviews(ctx context.Context, q git.Querier, now string, limit int) ([]Preview, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+previewColumns+` FROM pr_previews WHERE expires_at<=? ORDER BY expires_at, id LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	return scanPreviews(rows)
}

// orphans is everything the sweep has to reconcile that its clock will not reach: a preview that is still inside its
// lifetime but whose service or whose own resource row has gone away.
func orphans(ctx context.Context, q git.Querier, limit int) ([]Preview, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+previewColumns+` FROM pr_previews p
		WHERE (SELECT COUNT(*) FROM resources r WHERE r.id=p.preview_resource_id)=0
		   OR (SELECT COUNT(*) FROM resources r WHERE r.id=p.parent_resource_id)=0
		ORDER BY p.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanPreviews(rows)
}

func scanPreviews(rows *sql.Rows) ([]Preview, error) {
	defer rows.Close()
	var out []Preview
	for rows.Next() {
		var p Preview
		if err := rows.Scan(p.fields()...); err != nil {
			return nil, err
		}
		out = append(out, p.finish())
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func countPreviews(ctx context.Context, q git.Querier, parentID string) (int, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM pr_previews WHERE provider='github' AND parent_resource_id=?`, parentID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// volumeSeed is one bind volume the parent keeps data in, and the reason a preview needs the list: an application
// that writes to /data cannot run without it, and a preview that mounted the parent's directory would let an
// unmerged branch write into the real service's files.
type volumeSeed struct {
	name  string
	mount string
}

func readVolumes(ctx context.Context, q git.Querier, resourceID string) ([]volumeSeed, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, mount_path FROM volumes WHERE resource_id=? ORDER BY id`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []volumeSeed
	for rows.Next() {
		var v volumeSeed
		if err := rows.Scan(&v.name, &v.mount); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// List is every preview on the panel, soonest to expire first — the CLI's read, and the answer to "what is this host
// running for pull requests right now".
func (s *Service) List(ctx context.Context) ([]Preview, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+previewColumns+` FROM pr_previews ORDER BY expires_at, id`)
	if err != nil {
		return nil, err
	}
	return scanPreviews(rows)
}

// resourceExists is the sweep's question about a row it is about to act on. The preview resource and its parent are
// the same kind of thing — a resources row that may not be there any more — so one helper answers both.
func resourceExists(ctx context.Context, q git.Querier, id string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM resources WHERE id=?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// slugTaken and hostnameTaken are the build's occupancy questions, asked before anything is written so that a
// refusal leaves the transaction empty. Both names are UNIQUE for ordinary reasons — two services cannot share a
// slug, two routes cannot share a hostname — and an operator who made a service called `<app>-pr-7` is answering
// one of them, which is a sentence rather than a constraint error.

func slugTaken(ctx context.Context, q git.Querier, slug string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM resources WHERE slug=?`, slug).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func hostnameTaken(ctx context.Context, q git.Querier, hostname string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM domains WHERE hostname=?`, hostname).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
