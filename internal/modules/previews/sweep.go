package previews

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"

	"github.com/codeplo/codeplo/internal/store"
)

// sweepPerPass bounds one sweep call. A panel cannot have more previews than its limits allow times the services it
// has, so this is not a throttle on normal work — it is the guarantee that a database that has somehow grown a
// thousand rows still leaves the daemon's minute free for everything else, and the rows are ordered so the next pass
// carries on where this one stopped.
const sweepPerPass = 100

// Sweep reconciles previews with reality, and it is the only thing that owns reconciliation: pr_previews has no
// foreign keys, so nothing in the database tells a preview row its parent was deleted or its container was stopped
// by hand. Four states make a preview garbage, and the sweep looks for all four:
//
//   - its lifetime ended, which is the designed outcome and the one an abandoned branch reaches on its own;
//   - the service it previews is gone, so there is nothing left for it to be a preview of;
//   - its own resource row is gone, which means an operator deleted the preview from its page and the row that
//     survives is the panel's memory of a container that no longer exists;
//   - the feature was switched off, which is the operator asking for none of this to be running.
//
// The pass runs whether the feature flag is on or not. An operator who turns previews off has asked for them to
// stop happening, and live preview containers continuing to run under a flag that says they should not exist is the
// difference between a switch and a suggestion. Turning the base domain off or away does not stop the sweep either:
// a hostname that no longer resolves still describes a workload with the parent's credentials in it.
//
// Each preview is closed in its own transaction. A pass that stopped at the first failure would leave everything
// after it in the list waiting for a later minute, and a preview that cannot come down is worth the warning line it
// produces rather than a silent stall.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	if s.eng == nil || s.queue == nil {
		return 0, nil
	}
	now := s.ts(s.now().UTC())
	// The two reads are the whole pass: due is the clock, orphans is reality. A preview can be in both lists, which
	// is why the orphan list is worked first — its reasons are stronger, and closing one row twice is not on show.
	stale, err := orphans(ctx, s.db, sweepPerPass)
	if err != nil {
		return 0, err
	}
	due, err := duePreviews(ctx, s.db, now, sweepPerPass)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	removed, failed := 0, 0
	for _, p := range append(stale, due...) {
		if seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		note, err := s.reason(ctx, p, now)
		if err != nil {
			failed++
			slog.Warn("preview sweep could not read why a preview is due", "preview", p.ID, "err", err)
			continue
		}
		if note == "" {
			continue
		}
		if err := store.InTx(ctx, s.db, func(tx *sql.Tx) error { return s.close(ctx, tx, p, note) }); err != nil {
			failed++
			slog.Warn("preview sweep could not take a preview down", "preview", p.ID, "resource", p.ResourceID, "err", err)
			continue
		}
		removed++
	}
	if failed > 0 {
		return removed, errors.New("preview sweep could not take down " + strconv.Itoa(failed) + " previews")
	}
	return removed, nil
}

// reason is the sweep's decision for one preview, or "" when it is entitled to keep running. The sentence is what
// lands in the audit row, so an operator reading the log later can tell a branch that was abandoned from a service
// that was deleted.
func (s *Service) reason(ctx context.Context, p Preview, now string) (string, error) {
	parentOK, err := resourceExists(ctx, s.db, p.ParentResourceID)
	if err != nil {
		return "", err
	}
	if !parentOK {
		return "The service it previews was deleted", nil
	}
	resourceOK, err := resourceExists(ctx, s.db, p.ResourceID)
	if err != nil {
		return "", err
	}
	if !resourceOK {
		return "The preview was deleted from the panel", nil
	}
	// Turning the feature off is not a deletion of the service, so it comes after the two structural reasons and
	// before the clock: an operator who disables previews sees their previews go on the next pass, whichever way the
	// expiry was set.
	if !s.flagOn(ctx, flagPreviews) {
		return "Preview environments are disabled", nil
	}
	if p.ExpiresAt <= now {
		return "Its preview lifetime ended", nil
	}
	return "", nil
}
