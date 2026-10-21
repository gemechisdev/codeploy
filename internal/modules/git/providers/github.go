// Package providers knows the wire shape of the forges this panel talks to. Inbound, it converts a webhook payload
// into the package git domain types; outbound, it is the GitHub App's protocol — JWTs, installation tokens and the
// repository listing — and that is the only file here that makes a request. Keeping the dialect in one place means
// adding GitLab or Bitbucket later is a new file, not a new rule inside the gate. Nothing in this package touches
// SQL or the panel's policy.
package providers

import (
	"strings"

	"github.com/codeplo/codeplo/internal/modules/git"
)

// ParsePush reads a GitHub push event body (already decoded). Missing or wrongly typed fields degrade to empty
// values rather than an error: an unparseable payload simply matches no configured repository, and the receiver
// records why it deployed nothing.
func ParsePush(payload map[string]any) git.Push {
	p := git.Push{Branch: strings.TrimPrefix(asString(path(payload, "ref")), "refs/heads/")}
	p.Repo = firstNonEmpty(asString(path(payload, "repository", "ssh_url")), asString(path(payload, "repository", "clone_url")))
	p.Message = asString(path(payload, "head_commit", "message"))
	if sha := asString(path(payload, "after")); len(sha) >= 7 {
		p.SHA = sha[:7]
	}
	p.Changed, p.HasChanges = changedPaths(payload)
	return p
}

// changedPaths flattens the commit list the way GitHub sends it: added, modified and removed are all "changed"
// for the purpose of deciding whether a push touched the service.
func changedPaths(payload map[string]any) ([]string, bool) {
	commits, _ := payload["commits"].([]any)
	if len(commits) == 0 {
		return nil, false
	}
	seen := map[string]bool{}
	out := []string{}
	for _, c := range commits {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"added", "modified", "removed"} {
			list, _ := cm[key].([]any)
			for _, v := range list {
				if p, ok := v.(string); ok && p != "" && !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	return out, len(out) > 0
}

// path walks string keys of a decoded JSON object and returns the leaf as a string, or "".
func path(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		nm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = nm[k]
	}
	return cur
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// CanonicalRepo reduces the two forms a repository has (git@host:org/repo.git and https://host/org/repo) to one
// comparable key. The receiver needs it because GitHub reports the clone URL it used, which is rarely the exact
// string the operator typed into the panel.
func CanonicalRepo(u string) string {
	s := strings.TrimSpace(u)
	s = strings.TrimSuffix(s, ".git")
	if strings.HasPrefix(s, "git@") { // scp-style git@host:path, which has no scheme to strip
		s = "ssh://" + strings.Replace(s, ":", "/", 1)
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Drop userinfo, but only when it appears before the first slash (ssh://git@host/...).
	if j := strings.Index(s, "@"); j >= 0 && !strings.Contains(s[:j], "/") {
		s = s[j+1:]
	}
	host, rest := s, ""
	if j := strings.Index(s, "/"); j >= 0 {
		host, rest = s[:j], s[j+1:]
	}
	// The host is case-insensitive; the path is not.
	return strings.ToLower(host) + "/" + rest
}

// RepoMatches reports whether a payload's repository is the one a resource is configured to build from.
func RepoMatches(payloadRepo, configured string) bool {
	c := CanonicalRepo(configured)
	return c != "" && CanonicalRepo(payloadRepo) == c
}
