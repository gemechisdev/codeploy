package git

import (
	"strconv"
)

// PullRequest is the part of a GitHub pull_request event this panel acts on. It is a different shape from Push on
// purpose: a push tells the panel what happened to a branch it owns, while a pull request tells it what someone
// would like to happen, so the fields that decide anything here are the ones that establish whose code it is
// (HeadRepo against BaseRepo, Draft) and how long it stays relevant (Number, which the preview row is keyed by).
type PullRequest struct {
	Action    string // GitHub's action: opened, synchronize, reopened, closed, converted_to_draft, edited, …
	Number    int    // the repository-side pull request number; 0 when the payload carried none
	Title     string
	Draft     bool
	Merged    bool   // only meaningful with Action "closed": it says whether the branch landed
	HeadRef   string // the branch to build, e.g. "feature/login"
	HeadSHA   string // full sha of the head commit
	HeadRepo  string // where HeadRef lives; empty when the payload has no head repository (a deleted fork)
	BaseRef   string // the branch the request would change
	BaseRepo  string // the repository the request is against — the one the panel matches configured resources on
	CrossRepo bool   // the head repository is not the base one, i.e. the contributor worked in a fork
}

// PullRequestAction is what the receiver does with a delivery.
type PullRequestAction int

const (
	// PRIgnore leaves the service alone and says why, which is what the delivery row records.
	PRIgnore PullRequestAction = iota
	PRBuild
	PRTeardown
)

// pull request actions GitHub sends. Only these two groups change anything: the rest (edited, labeled, assigned,
// review_requested, …) are metadata on a request whose code is already built.
const (
	prActionClosed   = "closed"
	prActionDraft    = "converted_to_draft"
	prActionOpened   = "opened"
	prActionReady    = "ready_for_review"
	prActionReopened = "reopened"
	prActionSync     = "synchronize"
)

// DecidePullRequest is the gate order for a preview delivery, and it returns the action along with the reason it
// did not take one — a pull request can build, can be taken down, or can be left alone, where a push can only
// deploy or be ignored.
//
// The two refusals that have no equivalent in EvaluatePush are the fork and the draft. A preview is the parent
// service's own configuration run against someone's branch, credentials and environment variables included, so
// code from outside the repository would get the database password for the service it is proposing to change.
// That is why an unreviewed draft is refused for the same reason: the author has not asked for a review yet.
//
// Teardown is checked last and passes the build's refusals. "This branch is not safe to run" is never a reason to
// leave something running.
func DecidePullRequest(event string, src Source, p PullRequest) (PullRequestAction, string) {
	if event != "pull_request" {
		return PRIgnore, "Event " + strconv.Quote(event) + " is not handled"
	}
	if p.Number <= 0 {
		return PRIgnore, "The delivery carries no pull request number"
	}
	if p.Action == prActionClosed || p.Action == prActionDraft {
		return PRTeardown, ""
	}
	switch p.Action {
	case prActionOpened, prActionReopened, prActionReady, prActionSync:
	default:
		return PRIgnore, "Action " + strconv.Quote(p.Action) + " does not change the branch"
	}
	// The parser clears an illegal ref or sha rather than passing it on, so an empty one here is a payload that
	// named no buildable commit.
	if p.HeadRef == "" || p.HeadSHA == "" {
		return PRIgnore, "The delivery names no branch and commit this panel could clone"
	}
	if p.BaseRef == "" || deref(src.Branch) != p.BaseRef {
		return PRIgnore, "The pull request targets " + strconv.Quote(p.BaseRef) + ", not the configured branch " + strconv.Quote(deref(src.Branch))
	}
	if p.CrossRepo {
		return PRIgnore, "The head branch is in a fork, and a preview would run unreviewed code with this service's credentials"
	}
	if p.HeadRepo == "" {
		return PRIgnore, "The head repository is gone, so there is nothing to clone"
	}
	if p.Draft {
		return PRIgnore, "The pull request is a draft"
	}
	if SkipMarker(p.Title) {
		return PRIgnore, "Pull request title contains [skip deploy]"
	}
	return PRBuild, ""
}

// PullRequestNote is the sentence a teardown records: which of the two actions arrived, and whether the branch
// landed. It belongs next to the gate because the three cases are what an operator reads in the audit log months
// later to tell a merged pull request from an abandoned one — the preview row is gone by then either way, and this
// is the only thing that says why.
func PullRequestNote(p PullRequest) string {
	switch p.Action {
	case prActionDraft:
		return "The pull request was converted to a draft"
	case prActionClosed:
		if p.Merged {
			return "The pull request was merged"
		}
		return "The pull request was closed"
	}
	return "The pull request no longer wants a preview"
}
