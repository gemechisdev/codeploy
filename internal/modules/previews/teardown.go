package previews

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/deploy"
)

// Close takes the preview of one pull request down, in the caller's transaction, and reports whether there was one.
//
// The caller decides what the absence means. A `closed` delivery for a request that never produced a preview is not
// an error and not something to retry; it is the ordinary case of someone opening a draft, or a fork, or a request
// against another branch — all of which are refused at the gate, and all of which GitHub will still send a `closed`
// delivery for. So the receiver records the reason and answers 200, and the preview that was never built stays
// never built.
func (s *Service) Close(ctx context.Context, tx *sql.Tx, parentID string, number int, note string) (Preview, bool, error) {
	p, found, err := find(ctx, tx, parentID, number)
	if err != nil || !found {
		return Preview{}, false, err
	}
	return p, true, s.close(ctx, tx, p, note)
}

// close does the removal, and it is the same four statements the resource delete route runs, in the same order:
// domains first so the route cannot outlive the workload, then the resource (whose deployment, source, variable,
// credential and volume rows cascade with it), then the preview row, then the reap job that takes the containers and
// the images off the host.
//
// The image tags are read before the delete because they come from the deployment rows that cascade away with it —
// a preview that leaves its images behind has used disk for a branch nobody merged, and a preview that deletes
// someone else's tags has done something worse.
func (s *Service) close(ctx context.Context, tx *sql.Tx, p Preview, note string) error {
	// The row may already be gone: the sweep reads a list and then works through it, and an operator who deleted the
	// preview from its own page in between has already run every statement below. Re-running them would be harmless
	// except for the reap job, so the guard is here rather than in each statement.
	var one int
	switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM pr_previews WHERE id=?`, p.ID).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}

	var (
		images  []string
		slug    = slugFromHostname(p.Hostname)
		removed = false
	)
	ok, err := resourceExists(ctx, tx, p.ResourceID)
	if err != nil {
		return err
	}
	if ok {
		// Read before the delete: the tags come from the deployment rows that cascade away with the resource.
		images = s.eng.ResourceTags(ctx, p.ResourceID)
		if _, err := tx.ExecContext(ctx, `DELETE FROM domains WHERE resource_id=?`, p.ResourceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM resources WHERE id=?`, p.ResourceID); err != nil {
			return err
		}
		removed = true
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pr_previews WHERE id=?`, p.ID); err != nil {
		return err
	}
	// The reap follows every preview that had a directory of its own: containers and image tags first, then the
	// volume tree under the volumes root. When the resource row is already gone, the delete that removed it queued a
	// reap that did not ask for directories — a normal service keeps its data when its operator removes it, and only
	// a preview is disposable enough to have its files taken with it. This second job finds no containers, removes
	// the directory the first one left, and ends.
	//
	// The guard is the hostname: a preview slug is derived from its parent and its number, so the pull request
	// reopened after this one closed would be given the same directory. Deleting that directory would be this
	// transaction destroying a workload that exists now, so the reap keeps its hands off it.
	owns, err := s.stillOwnsHostname(ctx, tx, p)
	if err != nil {
		return err
	}
	if removed || slug != "" {
		if err := s.eng.EnqueueReap(ctx, tx, s.queue, deploy.ReapRequest{
			ResourceID: p.ResourceID, Slug: slug, Images: images, RemoveVolumeDirs: owns,
		}); err != nil {
			return err
		}
	}
	_, err = audit.Append(ctx, tx, audit.Entry{ActorType: "system", ActorID: "previews",
		Action: "preview.removed", TargetType: "resource", TargetID: p.ResourceID,
		Meta: map[string]any{"preview_id": p.ID, "parent_resource_id": p.ParentResourceID, "number": p.Number,
			"hostname": p.Hostname, "note": note, "images_queued": len(images), "resource_deleted": removed}})
	return err
}

// stillOwnsHostname asks whether this preview is still the only thing that claims its hostname. The row above it has
// just been deleted, so a surviving row means the name — and the directory named after it — belongs to a preview
// that exists now.
func (s *Service) stillOwnsHostname(ctx context.Context, tx *sql.Tx, p Preview) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pr_previews WHERE hostname=? AND id<>?`, p.Hostname, p.ID).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// slugFromHostname recovers the preview resource's slug from the hostname it was routed on.
//
// The slug is stored on the resource, and the resource may already be gone by the time the sweep reaches a preview —
// hostname is `<slug>.<base domain>`, and a preview slug cannot contain a dot, so the first label is the slug and the
// row that would have confirmed it may not exist. It is worth deriving rather than storing a second copy: the
// alternative is a container that keeps running with nothing left that knows it is disposable.
func slugFromHostname(hostname string) string {
	label := strings.SplitN(strings.ToLower(hostname), ".", 2)[0]
	if !hostnameLabelRe.MatchString(label) {
		return ""
	}
	return label
}
