package deploy

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/codeplo/codeplo/internal/modules/git/providers"
)

// statusTimeout bounds one forge report. The call is made from a deployment's own path and from boot recovery, and
// neither may wait on a network that has stopped answering: ten seconds is long enough for a working connection and
// short enough that a dead one cannot be mistaken for a deployment.
const statusTimeout = 10 * time.Second

// reportOutcome tells the forge where this deployment stands, reading the row the panel itself wrote.
//
// The row, not the engine's in-memory idea of it, because every caller arrives after something else may have had
// the last word: observe finalises a success, another process can cancel, boot recovery closes what a crash left
// open. A status that disagrees with the panel's own record is worse than no status.
//
// It cannot fail a deployment. This is a courtesy to the repository that pushed the commit — if GitHub is
// unreachable the deploy has still happened, and the deployment log says which of the two occurred.
func (e *Engine) reportOutcome(ctx context.Context, r *run) {
	if e.reportStatus == nil {
		return
	}
	// The bounded context is made before the first read, not around the HTTP call alone: a cancel arrives while a
	// deployment is still writing its own row, and a report that silently skipped the read because of it would log a
	// warning about a database that answered fine a moment later.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusTimeout)
	defer cancel()
	var status, sha, repo, name, resourceID string
	err := e.db.QueryRowContext(sctx, `SELECT d.status, COALESCE(d.commit_sha,''), COALESCE(s.repo_url,''), r.name, d.resource_id
		FROM deployments d JOIN resources r ON r.id=d.resource_id
		LEFT JOIN resource_sources s ON s.resource_id=r.id
		WHERE d.id=?`, r.id).Scan(&status, &sha, &repo, &name, &resourceID)
	if err != nil {
		slog.Warn("deploy: could not read the deployment to report it", "deployment", r.id, "err", err)
		return
	}
	if status == StQueued {
		return // nothing has begun; a commit marked pending before it is even claimed is noise
	}
	// A deployment that came from a registry image or a deploy hook has no commit, and a repository nothing was
	// built from has no status to carry. Saying so quietly is right; the alternative is a forge error per push.
	if sha == "" || repo == "" {
		return
	}
	state, desc := commitStatus(status, statusName(name))
	report := providers.CommitStatus{RepoURL: repo, SHA: sha, State: state, Description: desc,
		TargetURL: e.panelResourceURL(sctx, resourceID)}
	if err := e.reportStatus(sctx, report); err != nil {
		if r.logw != nil {
			r.logw.Sys("the repository was not told about this deployment: " + err.Error())
		}
		slog.Warn("deploy: commit status report failed", "deployment", r.id, "err", err)
		return
	}
	if r.logw != nil {
		r.logw.Sys("commit status reported to the repository: " + state)
	}
}

// commitStatus translates the panel's deployment states into GitHub's four.
//
// The panel's own error text is deliberately absent. A commit status is readable by everyone who can read the
// repository, while a build failure's message is git and compiler output from inside this host — paths, registry
// addresses, sometimes a line a script echoed. The sentence below points at the log the panel already redacts.
func commitStatus(status, name string) (string, string) {
	switch status {
	case StSucceeded:
		return providers.StatusSuccess, "codeplo deployed " + name
	case StCancelled:
		return providers.StatusFailure, "codeplo cancelled the deploy of " + name
	case StRolledBack:
		return providers.StatusFailure, "codeplo rolled " + name + " back: the new version failed its health checks"
	case StFailed:
		return providers.StatusFailure, "codeplo could not deploy " + name + ". See the deployment log in the panel."
	case StInterrupted:
		// Not a failure of the commit: the thing that was checking it stopped. GitHub's `error` is the state that
		// says so, and a human has to look rather than a branch protection rule deciding the commit is broken.
		return providers.StatusError, "codeplo stopped before finishing the deploy of " + name
	default:
		return providers.StatusPending, "codeplo is deploying " + name
	}
}

// nameRe is the resource-name rule the API already enforces (slugRe). A resource name goes into a commit status,
// which crosses a network boundary as free text, so it is checked again here rather than trusted to have come from
// that route: it can be a row an operator wrote by hand, or one an older build allowed through.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// statusName is the resource's name as it may be published: the slug itself when it is one, and a neutral word when
// the row holds something that is not a slug. The id never appears — it is a real identifier of a private panel,
// and a repository's status list is not where it belongs.
func statusName(raw string) string {
	if nameRe.MatchString(raw) {
		return raw
	}
	return "a service"
}

// panelResourceURL is the address a commit status links to: the resource page, which is where a deployment's log
// and its rollback live in this UI. Empty when the panel has no domain set — the engine will not invent a host, and
// a status that links nowhere is still a status.
func (e *Engine) panelResourceURL(ctx context.Context, resourceID string) string {
	var host string
	if err := e.db.QueryRowContext(ctx, `SELECT hostname FROM domains WHERE kind='panel' LIMIT 1`).Scan(&host); err != nil || host == "" {
		return ""
	}
	return "https://" + host + "/resources/" + resourceID
}
