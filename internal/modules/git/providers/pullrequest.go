package providers

import (
	"github.com/codeplo/codeplo/internal/modules/git"
)

// ParsePullRequest reads a GitHub pull_request event body (already decoded) and returns the reason the delivery
// cannot be used at all, or "" .
//
// Only two things are refused here rather than degraded to empty like ParsePush does its fields: no pull request
// object, and no number. Both mean the payload describes no request the panel could name, so there is nothing for
// the receiver to match, build or take down — and a number is what the preview row is keyed by, so a delivery
// without one cannot be reconciled with anything later. Everything else is allowed to be missing, because GitHub
// itself sends partial payloads (a deleted fork's head repository is null), and "which missing fields matter" is a
// policy question the gate answers, not a parsing one.
//
// Two fields are actively discarded rather than passed through: a head ref that is not a legal branch name, and a
// sha that is not a sha. They would otherwise reach a clone and a commit status, both of which build a command or a
// URL out of them.
func ParsePullRequest(payload map[string]any) (git.PullRequest, string) {
	pr, ok := payload["pull_request"].(map[string]any)
	if !ok {
		return git.PullRequest{}, "The delivery has no pull request object"
	}
	p := git.PullRequest{
		Action:   asString(path(payload, "action")),
		Number:   prNumber(payload, pr),
		Title:    asString(path(pr, "title")),
		Draft:    asBool(path(pr, "draft")),
		Merged:   asBool(path(pr, "merged")),
		HeadRef:  asString(path(pr, "head", "ref")),
		HeadSHA:  asString(path(pr, "head", "sha")),
		HeadRepo: firstNonEmpty(asString(path(pr, "head", "repo", "ssh_url")), asString(path(pr, "head", "repo", "clone_url"))),
		BaseRef:  asString(path(pr, "base", "ref")),
		// The base repository is the one the operator configured a source for. `repository` is the same thing for a
		// pull_request event, and using it when base.repo is missing keeps a payload that only carries the
		// repository from being read as a delivery about nothing.
		BaseRepo: firstNonEmpty(asString(path(pr, "base", "repo", "ssh_url")), asString(path(pr, "base", "repo", "clone_url")),
			asString(path(payload, "repository", "ssh_url")), asString(path(payload, "repository", "clone_url"))),
	}
	if p.Number <= 0 {
		return git.PullRequest{}, "The delivery has no pull request number"
	}
	if !git.ValidBranch(p.HeadRef) {
		p.HeadRef = ""
	}
	if !shaRe.MatchString(p.HeadSHA) {
		p.HeadSHA = ""
	}
	// A fork is a head repository that is not the base one. GitHub's own `fork` flag says it, and the URLs say it
	// too: the flag is checked first because it is the repository's own answer, and the comparison catches the
	// transfer that happened after GitHub set the flag.
	p.CrossRepo = asBool(path(pr, "head", "repo", "fork")) ||
		(p.HeadRepo != "" && p.BaseRepo != "" && !RepoMatches(p.HeadRepo, p.BaseRepo))
	return p, ""
}

// prNumber reads the request number from the top level, which is where GitHub puts it, and falls back to the
// nested copy older integrations send.
func prNumber(payload, pr map[string]any) int {
	if n := asInt(payload["number"]); n > 0 {
		return n
	}
	if n := asInt(pr["number"]); n > 0 {
		return n
	}
	return 0
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// asInt reads a JSON number. encoding/json puts every number in a float64, so a whole-value check is what separates
// a real number from a payload that sent a string or an object where a count belongs.
func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		if n > 0 && n == float64(int(n)) {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	}
	return 0
}
