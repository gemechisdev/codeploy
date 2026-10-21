package providers

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The App's wire budget. A JWT lives ten minutes because that is the longest GitHub accepts; the token it buys
// lives an hour, and it is refreshed two minutes early so a clone that starts at 09:59 does not fail at 10:01.
const (
	jwtTTL        = 9 * time.Minute
	tokenMargin   = 2 * time.Minute
	tokenFallback = 50 * time.Minute
	reposTTL      = 10 * time.Minute
	reposPageSize = 100
	reposMaxPages = 100
	callTimeout   = 30 * time.Second
	bodyLimit     = 8 << 20
)

// InstallationRepo is one repository the installation can read — the data behind a repository picker.
type InstallationRepo struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

// GitHubApp speaks the GitHub App wire protocol: a self-signed RS256 JWT exchanges for a short-lived installation
// token, and that token reads the installation's repositories and clones with them.
//
// It holds no state that outlives the process. A token in a database would be a credential another reader of the
// panel could take; a token in memory dies with the daemon, and a restart costs one API call.
type GitHubApp struct {
	AppID          string
	InstallationID string
	Key            *rsa.PrivateKey
	// APIURL is the REST base: https://api.github.com, or https://host/api/v3 for GitHub Enterprise Server.
	APIURL string
	// WebhookSecret is the App-level webhook secret. One secret signs every repository's delivery, which is why the
	// receiver tries it when the named resource has no secret of its own: an installed App posts to one URL for the
	// whole installation, and a per-resource secret cannot exist for a repository the panel has never been told about.
	WebhookSecret []byte

	// HTTP and Now are injectable so a test can freeze the clock and answer with a canned response.
	HTTP *http.Client
	Now  func() time.Time

	mu    sync.Mutex
	token string
	until time.Time
	repos *repoCache
}

type repoCache struct {
	list []InstallationRepo
	// granted is the lowercase `host/org/repo` of each listed repository.
	granted map[string]bool
	// allRepos is GitHub's `repository_selection: all` — the installation covers every repository the account has
	// and every one it creates, so a repository missing from this page is not a repository the App cannot read.
	allRepos bool
	fetched  time.Time
	// capped records that the listing stopped at reposMaxPages, so a huge account is reported rather than silently
	// treated as "this repository is not in the installation".
	capped bool
}

// NewGitHubApp builds the client from the values the environment carried. Every rejection here is a boot-time
// message: a key that will not parse or an id that is not a number can never produce a token, and finding that out
// inside a failed build tells the operator nothing.
func NewGitHubApp(appID, installationID string, keyPEM []byte, apiURL string) (*GitHubApp, error) {
	a := &GitHubApp{AppID: strings.TrimSpace(appID), InstallationID: strings.TrimSpace(installationID)}
	for _, f := range []struct {
		name, value string
	}{{"CODEPLO_GITHUB_APP_ID", a.AppID}, {"CODEPLO_GITHUB_APP_INSTALLATION_ID", a.InstallationID}} {
		if f.value == "" {
			return nil, fmt.Errorf("%s is empty", f.name)
		}
		if _, err := strconv.ParseUint(f.value, 10, 64); err != nil {
			return nil, fmt.Errorf("%s must be the numeric id GitHub shows (got %q)", f.name, f.value)
		}
	}
	key, err := parseRSAKey(keyPEM)
	if err != nil {
		return nil, err
	}
	a.Key = key
	if a.APIURL, err = normaliseAPIURL(apiURL); err != nil {
		return nil, err
	}
	a.HTTP = &http.Client{Timeout: callTimeout}
	a.Now = time.Now
	return a, nil
}

// parseRSAKey reads the .pem GitHub downloads. Both containers GitHub itself produces are accepted, and an
// encrypted block is refused with the reason rather than a nil-pointer panic three calls later.
func parseRSAKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("the GitHub App private key is not a PEM block: paste the contents of the .pem GitHub downloaded, between its -----BEGIN and -----END lines")
	}
	if len(block.Headers["Proc-Type"]) > 0 {
		return nil, errors.New("the GitHub App private key is passphrase-encrypted; GitHub's downloaded key is not, so this is the wrong file")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the GitHub App private key could not be read: %w", err)
		}
		return k, nil
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the GitHub App private key could not be read: %w", err)
		}
		k, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("the GitHub App private key is a %T, and GitHub's App keys are RSA", parsed)
		}
		return k, nil
	default:
		return nil, fmt.Errorf("the GitHub App private key is a PEM %q block; expected an RSA private key", block.Type)
	}
}

// normaliseAPIURL refuses anything that is not an https origin: the JWT and the installation token both cross this
// connection, and a base URL with a path or query would silently misroute the request while looking configured.
func normaliseAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "https://api.github.com", nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return "", fmt.Errorf("CODEPLO_GITHUB_APP_API_URL: %w", err)
	case u.Scheme != "https":
		return "", fmt.Errorf("CODEPLO_GITHUB_APP_API_URL must be https (got %q): an App token crosses it in plaintext headers otherwise", raw)
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return "", fmt.Errorf("CODEPLO_GITHUB_APP_API_URL must be a bare origin such as https://git.example.com/api/v3 (got %q)", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// CloneHost is the host a repository URL is expected to carry for this App: github.com for the public API, and the
// Enterprise host otherwise. The API host is not the clone host, and conflating them is why a GHES install either
// clones with no credential or never gets asked for one.
func (a *GitHubApp) CloneHost() string {
	h := strings.ToLower(a.APIURL[strings.Index(a.APIURL, "://")+3:])
	if i := strings.Index(h, "/"); i >= 0 {
		h = h[:i]
	}
	return strings.TrimPrefix(h, "api.")
}

// OnHost says whether a repository URL points at this App's forge at all. It is offline and cheap, and it is what
// keeps a panel that installs a GitHub App from ever calling GitHub's API for a repository on GitLab.
func (a *GitHubApp) OnHost(repoURL string) bool {
	c := CanonicalRepo(repoURL)
	return strings.HasPrefix(c, strings.ToLower(a.CloneHost())+"/")
}

// repoPath is the `org/repo` part of a clone URL, lowercased. GitHub's full_name is the same string with its own
// capitalisation, and a repository can be renamed to a different case of itself, so the comparison ignores it.
func repoPath(repoURL string) string {
	c := strings.ToLower(CanonicalRepo(repoURL))
	if i := strings.Index(c, "/"); i >= 0 {
		return c[i+1:]
	}
	return c
}

func (a *GitHubApp) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Token returns an installation token that is still valid, minting one when the cached copy has gone or is about
// to. The mutex is held across the call on purpose: two builds that start together should exchange one JWT, not
// two, and GitHub invalidates nothing but rate-limits both.
func (a *GitHubApp) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.now().Before(a.until) {
		return a.token, nil
	}
	jwt, err := a.signedJWT()
	if err != nil {
		return "", err
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	path := "/applications/" + a.InstallationID + "/access_tokens"
	if err := a.call(ctx, jwt, "POST", path, map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("GitHub answered the token request for installation %s without a token", a.InstallationID)
	}
	until := a.now().Add(tokenFallback)
	if exp, err := time.Parse(time.RFC3339, out.ExpiresAt); err == nil {
		until = exp.Add(-tokenMargin)
	}
	if left := time.Until(until); left < 30*time.Second {
		// A token already on its way out is not worth caching: the next call would find it expired, and the one
		// after that would look like a rate limit. Asking again is one request per build, which is the real floor.
		until = a.now()
	}
	a.token, a.until = out.Token, until
	return a.token, nil
}

// TokenValidUntil is when the cached installation token stops working, or the zero time when none is cached. The
// CLI reports it so an operator can see the exchange succeeded without the token itself ever being printed — a
// credential in a terminal transcript is a credential in a shell history file and a support ticket.
func (a *GitHubApp) TokenValidUntil() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.until
}

// signedJWT is the RS256 assertion GitHub accepts as the App itself. `iat` is pushed a minute into the past because
// GitHub rejects a token issued in the future and a clock a few seconds off is ordinary.
func (a *GitHubApp) signedJWT() (string, error) {
	iat := a.now().Add(-time.Minute)
	enc := base64.RawURLEncoding
	head, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iat": iat.Unix(), "exp": iat.Add(jwtTTL).Unix(), "iss": a.AppID})
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("the GitHub App key could not sign a request: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Repositories lists what the installation can read, from the cache while it is fresh.
func (a *GitHubApp) Repositories(ctx context.Context) ([]InstallationRepo, error) {
	c, err := a.repoCache(ctx)
	if err != nil {
		return nil, err
	}
	return c.list, nil
}

// Covers says whether this App is the right credential for a repository: the same forge, and either an
// all-repositories installation or a repository in the listed set.
func (a *GitHubApp) Covers(ctx context.Context, repoURL string) (bool, error) {
	if !a.OnHost(repoURL) {
		return false, nil
	}
	c, err := a.repoCache(ctx)
	if err != nil {
		return false, err
	}
	if c.allRepos {
		return true, nil
	}
	// A listing that stopped at the page cap cannot say "no": the repository may be past it. Cloning anyway costs a
	// failed git auth; refusing costs a deploy that would have worked.
	if c.capped {
		return true, nil
	}
	return c.granted[repoPath(repoURL)], nil
}

// TokenFor is the one call the deployment engine needs: a credential for this repository, and whether this App is
// the thing that should be providing it. A repository on another host, or one outside the installation, is not an
// error — it is the panel's existing behaviour, which is to clone with what the resource itself carries.
func (a *GitHubApp) TokenFor(ctx context.Context, repoURL string) (string, bool, error) {
	covered, err := a.Covers(ctx, repoURL)
	if err != nil || !covered {
		return "", false, err
	}
	tok, err := a.Token(ctx)
	if err != nil {
		return "", false, err
	}
	return tok, true, nil
}

// The four states GitHub accepts for a commit status. They are its vocabulary, not the panel's: a deployment the
// panel calls `interrupted` is GitHub's `error`, because nothing about the commit failed — the system that was
// supposed to check it did.
const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusError   = "error"

	// StatusContext is the check's name in GitHub's UI and the key a later status overwrites. One context per panel
	// means a redeploy updates the same line rather than stacking five.
	StatusContext = "codeplo/deploy"
	// statusDescription is GitHub's own limit on the description line.
	statusDescription = 145
)

// shaRe bounds what goes into a URL path segment. A deployment row's commit sha is git's own output, but this is
// still an identifier crossing a request boundary, and `../` has no business in it.
var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// repoNameRe is GitHub's `org/repo`: exactly two segments, each a legal repository name. A nested path is not a
// repository this endpoint can address, and refusing it is better than posting a status to whatever `a/b/c` means.
var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// CommitStatus is one commit's report: which repository and sha, how the deployment stands, and the panel's address
// for it. TargetURL is optional — a panel with no domain set has no address to hand out, and a status with no link
// beats a link to a host that does not exist.
type CommitStatus struct {
	RepoURL, SHA, State, Description, TargetURL string
}

// PostStatus writes one commit status: `POST /repos/{org}/{repo}/statuses/{sha}` with the installation's token.
//
// The description is the caller's sentence and this function treats it as untrusted: it is flattened, cut at the
// byte GitHub counts rather than the byte it lands on, and never filled from a deployment's error text by the
// engine. Anything the panel has redacted in its own logs must not reach a page every reader of the repository can
// open.
func (a *GitHubApp) PostStatus(ctx context.Context, s CommitStatus) error {
	repo := repoPath(s.RepoURL)
	if !repoNameRe.MatchString(repo) {
		return fmt.Errorf("%q is not an org/repository this panel can report against", s.RepoURL)
	}
	sha := strings.ToLower(strings.TrimSpace(s.SHA))
	if !shaRe.MatchString(sha) {
		return fmt.Errorf("%q is not a commit sha, so nothing can be reported for it", s.SHA)
	}
	state := s.State
	switch state {
	case StatusPending, StatusSuccess, StatusFailure, StatusError:
	default:
		return fmt.Errorf("%q is not a commit status GitHub accepts", s.State)
	}
	bearer, err := a.Token(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{"state": state, "context": StatusContext, "description": clipStatus(s.Description)}
	if s.TargetURL != "" {
		body["target_url"] = s.TargetURL
	}
	return a.call(ctx, bearer, "POST", "/repos/"+repo+"/statuses/"+sha, body, nil)
}

// clipStatus makes a sentence fit GitHub's line: control characters and runs of whitespace collapse (a status is
// one line in a table), and the cut is at a rune boundary so a clipped UTF-8 description stays readable.
func clipStatus(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 {
			return ' '
		}
		return r
	}, s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "deployed by codeplo"
	}
	if len(s) <= statusDescription {
		return s
	}
	cut := statusDescription
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut-1]) + "…"
}

// repoCache returns the installation's repositories, fetching them when the cache has aged out. The lock is
// released while GitHub is being read: Token() takes the same lock to mint the bearer, and holding it across an
// HTTP call would be a deadlock that only appears the first time a deployment lists repositories.
func (a *GitHubApp) repoCache(ctx context.Context) (*repoCache, error) {
	if c := a.cachedRepos(); c != nil {
		return c, nil
	}
	bearer, err := a.Token(ctx)
	if err != nil {
		return nil, err
	}
	c := &repoCache{granted: map[string]bool{}, fetched: a.now()}
	for page := 1; page <= reposMaxPages; page++ {
		var out struct {
			TotalCount          int                `json:"total_count"`
			RepositorySelection string             `json:"repository_selection"`
			Repositories        []InstallationRepo `json:"repositories"`
		}
		path := fmt.Sprintf("/installation/repositories?per_page=%d&page=%d", reposPageSize, page)
		if err := a.call(ctx, bearer, "GET", path, nil, &out); err != nil {
			return nil, err
		}
		if page == 1 {
			c.allRepos = out.RepositorySelection == "all"
		}
		for _, r := range out.Repositories {
			if r.FullName == "" {
				continue
			}
			c.list = append(c.list, r)
			c.granted[strings.ToLower(strings.TrimSuffix(r.FullName, ".git"))] = true
		}
		if len(out.Repositories) < reposPageSize || len(c.list) >= out.TotalCount {
			return a.retain(c), nil
		}
	}
	c.capped = true
	return a.retain(c), nil
}

// cachedRepos answers from the cache, or nil to say it has aged out and someone has to go and read the installation
// again. Only an aged-out cache sends a request: a build that finds a fresh list never waits on the network for a
// repository it can already name.
func (a *GitHubApp) cachedRepos() *repoCache {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.repos == nil || a.now().After(a.repos.fetched.Add(reposTTL)) {
		return nil
	}
	return a.repos
}

func (a *GitHubApp) retain(c *repoCache) *repoCache {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Another build may have finished the same listing while this one was in flight; the older cache wins, so a
	// stale page is never remembered for a fresh one.
	if a.repos == nil || a.repos.fetched.Before(c.fetched) {
		a.repos = c
	}
	return a.repos
}

// call is the whole HTTP surface this type needs. The error text is GitHub's own `message`, which is the sentence
// an operator can act on; the request body is never echoed, and no response containing a token is logged.
func (a *GitHubApp) call(ctx context.Context, bearer, method, path string, in any, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.APIURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
	if err != nil {
		return fmt.Errorf("%s %s: reading the response: %w", method, path, err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s%s answered %s: %s%s", method, path, resp.Status, gitHubMessage(raw), a.hint(resp.StatusCode, path))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s returned something that is not JSON: %w", method, path, err)
	}
	return nil
}

// gitHubMessage pulls GitHub's explanation out of an error body. A body that is not the JSON error shape GitHub
// documents is reduced to its first line, so a proxy's HTML error page cannot become the deployment log.
func gitHubMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	line := strings.TrimSpace(strings.SplitN(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '<' || r == '>' {
			return ' '
		}
		return r
	}, string(raw)), "\n", 2)[0])
	if line == "" {
		return "the response had no explanation"
	}
	if len(line) > 200 {
		line = line[:200] + "…"
	}
	return line
}

// hint turns the two statuses an operator can only fix outside this panel into instructions.
func (a *GitHubApp) hint(status int, path string) string {
	switch {
	case status == 401 && strings.HasPrefix(path, "/applications/"):
		return fmt.Sprintf(" (a 401 here means the private key does not belong to app id %s)", a.AppID)
	case status == 404 && strings.HasPrefix(path, "/applications/"):
		return fmt.Sprintf(" (a 404 here means installation %s does not exist: has the App been uninstalled, or is that the app id rather than the installation id from /settings/installations/<id>?)", a.InstallationID)
	case (status == 404 || status == 422) && strings.Contains(path, "/statuses/"):
		return " (GitHub has no such commit to put a status on: the branch was force-pushed or deleted after the build, or the sha came from a repository the App cannot see)"
	case status == 403 && strings.Contains(path, "/statuses/"):
		return " (a 403 here is not a rate limit: the App lacks the commit statuses: write permission, so grant it on the App's settings page and accept the refreshed installation request)"
	case status == 403 || status == 429:
		return " (GitHub rate limits the App's credential exchange too; try again after the limit window, or narrow the installation to fewer repositories)"
	}
	return ""
}
