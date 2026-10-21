package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/authz"
	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/modules/git"
	"github.com/codeplo/codeplo/internal/modules/git/providers"
	"github.com/codeplo/codeplo/internal/modules/previews"
)

// maxWebhookBody bounds a webhook payload. GitHub push events are under 25 KiB in practice; the cap is what stops
// an anonymous POST from allocating a gigabyte before the HMAC has been checked.
const maxWebhookBody = 1 << 20

func (s *Server) gitRoutes() []RouteDef {
	return []RouteDef{
		{Method: "GET", Path: "/git/connections", Perm: "git:write", Module: "git", Handler: s.listConnections},
		{Method: "POST", Path: "/git/connections", Perm: "git:write", Module: "git", Handler: s.createConnection},
		{Method: "GET", Path: "/resources/{id}/source", Perm: "resource:read", Module: "git", Handler: s.getResourceSource},
		{Method: "PUT", Path: "/resources/{id}/source", Perm: "git:write", Module: "git", Handler: s.putResourceSource},
		{Method: "POST", Path: "/resources/{id}/deploy-key", Perm: "git:write", Module: "git", Handler: s.createDeployKey},
		{Method: "GET", Path: "/resources/{id}/deploy-key", Perm: "resource:read", Module: "git", Handler: s.readDeployKey},
		{Method: "GET", Path: "/resources/{id}/deploy-hook", Perm: "hook:manage", Module: "git", Handler: s.getDeployHook},
		{Method: "POST", Path: "/resources/{id}/deploy-hook", Perm: "hook:manage", Module: "git", Handler: s.createDeployHook},
		{Method: "POST", Path: "/resources/{id}/deploy-hook/rotate", Perm: "hook:manage", Module: "git", Handler: s.rotateDeployHook},
		{Method: "GET", Path: "/resources/{id}/webhook", Perm: "hook:manage", Module: "git", Handler: s.getWebhook},
		{Method: "POST", Path: "/resources/{id}/webhook", Perm: "hook:manage", Module: "git", Handler: s.createWebhook},
		{Method: "GET", Path: "/webhooks/deliveries", Perm: "hook:manage", Module: "git", Handler: s.listDeliveries},
		// The two receivers are public because their caller is GitHub or a CI job, not a signed-in operator. They
		// authenticate by HMAC signature and by unguessable token respectively, and the guard rate-limits both.
		{Method: "POST", Path: "/webhooks/github", Perm: authz.Public, Module: "git", Handler: s.receiveGithub},
		{Method: "POST", Path: "/hooks/deploy/{token}", Perm: authz.Public, Module: "git", Handler: s.receiveDeployHook},
	}
}

// gitSvc returns the git service, or answers for it. Unlike the deploy engine it holds no runtime connection, so
// a missing service means the daemon was built without this phase rather than that Docker is down.
func (s *Server) gitSvc(w http.ResponseWriter, r *http.Request) (*git.Service, bool) {
	if !s.moduleOn(w, r, "git") {
		return nil, false
	}
	if s.Git == nil {
		writeErr(w, 500, "internal", "The git module is not available on this daemon", nil)
		return nil, false
	}
	return s.Git, true
}

// gitErr maps the git package's three failure kinds onto the envelope the contract defines, so the rules live in
// one place and a handler never has to remember which of them is a 404.
func (s *Server) gitErr(w http.ResponseWriter, err error) {
	var fe git.FieldErrors
	if errors.As(err, &fe) {
		invalid(w, fe)
		return
	}
	var nf git.NotFound
	if errors.As(err, &nf) {
		writeErr(w, 404, "not_found", nf.Error(), nil)
		return
	}
	var cf git.Conflict
	if errors.As(err, &cf) {
		writeErr(w, 409, "conflict", cf.Msg, nil)
		return
	}
	if errors.Is(err, git.ErrSealedKey) {
		writeErr(w, 500, "internal", "A stored secret cannot be opened with this daemon's master key", nil)
		return
	}
	internalErr(w, err)
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	items, err := g.ListConnections(r.Context(), g.DB)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	var b struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	out := git.Connection{}
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		c, err := g.CreateConnection(r.Context(), tx, b.Name)
		if err != nil {
			return audit.Event{}, err
		}
		out = c
		return s.rec(r.Context(), tx, r, p, "git.connection_created", "git_connection", c.ID, map[string]any{"name": c.Name})
	})
	if txErr != nil {
		s.gitErr(w, txErr)
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) getResourceSource(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	src, err := g.GetSource(r.Context(), g.DB, cur.ID)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, src)
}

// putResourceSource stores a source only if the engine could act on it. ValidateSource says whether the request is
// well formed; VerifySource says whether the deploy key behind it exists and opens, which is the difference between
// a panel that saves a configuration and one that saves a build that cannot start.
func (s *Server) putResourceSource(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	p := from(r)
	var in git.Source
	if !decode(w, r, &in) {
		return
	}
	src, err := g.ValidateSource(r.Context(), g.DB, cur.ID, in)
	if err != nil {
		s.gitErr(w, err)
		return
	}
	if check := g.VerifySource(r.Context(), g.DB, cur.ID, src); !check.OK {
		s.gitErr(w, git.UnreachableSourceError(check.Message))
		return
	}
	out := src
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if err := g.PutSource(r.Context(), tx, cur.ID, src); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "source.updated", "resource", cur.ID, map[string]any{
			"kind": src.Kind, "repo_url": src.RepoURL, "branch": src.Branch,
		})
	})
	if txErr != nil {
		s.gitErr(w, txErr)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": cur.ID})
	writeJSON(w, 200, out)
}

func (s *Server) createDeployKey(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	p := from(r)
	out := git.DeployKey{}
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		k, err := g.GenerateDeployKey(r.Context(), tx, cur.ID, cur.Name)
		if err != nil {
			return audit.Event{}, err
		}
		out = k
		// The audit row records the fingerprint and nothing else: it has to stay useful after the key is replaced,
		// and a signature of the private half in the audit log would defeat the sealing.
		return s.rec(r.Context(), tx, r, p, "deploy_key.created", "resource", cur.ID, map[string]any{"fingerprint": k.Fingerprint})
	})
	if txErr != nil {
		s.gitErr(w, txErr)
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) readDeployKey(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	k, err := g.GetDeployKey(r.Context(), g.DB, cur.ID)
	if err != nil {
		s.gitErr(w, err)
		return
	}
	writeJSON(w, 200, k)
}

// hookImageRef is the image the resource currently names, which is what the hook's tag policy is derived from.
func hookImageRef(cur resourceRow) (string, error) {
	cfg, err := deploy.ParseConfig(cur.CfgJSON)
	if err != nil {
		return "", err
	}
	if cfg.Image.Ref != nil {
		return *cfg.Image.Ref, nil
	}
	return "", nil
}

func (s *Server) getDeployHook(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h, err := g.GetHook(r.Context(), g.DB, cur.ID)
	if err != nil {
		s.gitErr(w, err)
		return
	}
	writeJSON(w, 200, h)
}

func (s *Server) createDeployHook(w http.ResponseWriter, r *http.Request) {
	s.issueDeployHook(w, r, false)
}

func (s *Server) rotateDeployHook(w http.ResponseWriter, r *http.Request) {
	s.issueDeployHook(w, r, true)
}

// issueDeployHook is the create and rotate paths together: they differ in whether an existing hook is a conflict
// or the thing being replaced, and they must agree on everything else -- same policy derivation, same once-only
// token, same audit shape.
func (s *Server) issueDeployHook(w http.ResponseWriter, r *http.Request, rotate bool) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	p := from(r)
	ref, err := hookImageRef(cur)
	if err != nil {
		internalErr(w, err)
		return
	}
	out := git.HookCreated{}
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		var h git.HookCreated
		if rotate {
			h, err = g.RotateHook(r.Context(), tx, cur.ID, ref, cur.Slug)
		} else {
			h, err = g.CreateHook(r.Context(), tx, cur.ID, ref, cur.Slug)
		}
		if err != nil {
			return audit.Event{}, err
		}
		out = h
		action := "deploy_hook.created"
		if rotate {
			action = "deploy_hook.rotated"
		}
		return s.rec(r.Context(), tx, r, p, action, "resource", cur.ID, map[string]any{"token_prefix": h.Hook.TokenPrefix})
	})
	if txErr != nil {
		s.gitErr(w, txErr)
		return
	}
	status := 201
	if rotate {
		status = 200
	}
	writeJSON(w, status, out)
}

func (s *Server) getWebhook(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	info, err := g.GetWebhook(r.Context(), g.DB, cur.ID)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	p := from(r)
	out := git.WebhookCreated{}
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		wh, err := g.CreateWebhook(r.Context(), tx, cur.ID)
		if err != nil {
			return audit.Event{}, err
		}
		out = wh
		return s.rec(r.Context(), tx, r, p, "webhook.created", "resource", cur.ID, map[string]any{"secret_prefix": out.Webhook.SecretPrefix})
	})
	if txErr != nil {
		s.gitErr(w, txErr)
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	limit, cursor := page(r)
	items, next, err := g.ListDeliveries(r.Context(), g.DB, r.URL.Query().Get("resource_id"), limit, cursor)
	if err != nil {
		internalErr(w, err)
		return
	}
	var nextCursor any
	if next != "" {
		nextCursor = next
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nextCursor})
}

// readSignedBody reads the request body once, as bytes, because the HMAC is over the exact octets GitHub signed.
// Decoding happens after, from the same buffer.
func readSignedBody(w http.ResponseWriter, r *http.Request) (raw []byte, payload map[string]any, ok bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		invalid(w, map[string]string{"body": "Body too large or unreadable"})
		return nil, nil, false
	}
	payload = map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		invalid(w, map[string]string{"body": "Invalid JSON body"})
		return nil, nil, false
	}
	return body, payload, true
}

// findSourceByRepo resolves the repository a payload names to the resource configured to build from it. The
// comparison is canonical (ssh vs https, .git suffix, host case) because GitHub reports the URL it pushed to,
// which is rarely the string the operator typed.
//
// When several resources share one repository the lowest id wins, which is the delivery table's constraint: a
// delivery row carries a single resource, so this endpoint deploys at most one service per push.
//
// Preview resources are excluded rather than left to that order. A preview shares its parent's repository URL and its
// source row, so it is a candidate for every push to that repository, and it would only ever lose by the accident
// that its id was minted later — and a preview that deployed off a push to the base branch would be building the
// wrong commit under the wrong name. Its head ref is what it builds, and only a pull_request delivery says which one.
func (s *Server) findSourceByRepo(ctx context.Context, g *git.Service, repo string) (string, git.Source, bool, error) {
	if repo == "" {
		return "", git.Source{}, false, nil
	}
	rows, err := g.DB.QueryContext(ctx, `SELECT resource_id, repo_url FROM resource_sources
		WHERE repo_url IS NOT NULL AND repo_url<>''
		  AND resource_id NOT IN (SELECT preview_resource_id FROM pr_previews)
		ORDER BY resource_id`)
	if err != nil {
		return "", git.Source{}, false, err
	}
	id := ""
	for rows.Next() {
		var rid, url string
		if err := rows.Scan(&rid, &url); err != nil {
			rows.Close()
			return "", git.Source{}, false, err
		}
		if providers.RepoMatches(repo, url) {
			id = rid
			break
		}
	}
	scanErr := rows.Err()
	// Closed before the next read rather than deferred: the pool is four connections and this runs on a public
	// endpoint, so a held cursor would starve every other query while a burst of pushes is being checked.
	rows.Close()
	if scanErr != nil {
		return "", git.Source{}, false, scanErr
	}
	if id == "" {
		return "", git.Source{}, false, nil
	}
	src, err := g.GetSource(ctx, g.DB, id)
	if err != nil {
		return "", git.Source{}, false, err
	}
	return id, src, true, nil
}

// errReplay unwinds a receiver transaction when another concurrent delivery already claimed the delivery id, so
// neither of them deploys and the loser reports the conflict.
var errReplay = errors.New("delivery already claimed")

// receiveGithub is the public push receiver. Its whole job is to be unforgeable: the signature is verified over
// the exact bytes before the payload is trusted to name a repository, the delivery id is claimed in the same
// transaction as the deployment it triggers, and a push that fails any gate records why instead of deploying.
func (s *Server) receiveGithub(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	raw, payload, ok := readSignedBody(w, r)
	if !ok {
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event == "" {
		event = "push"
	}
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	sig := r.Header.Get("X-Hub-Signature-256")
	p := providers.ParsePush(payload)

	rid, src, found, err := s.findSourceByRepo(r.Context(), g, p.Repo)
	if err != nil {
		internalErr(w, err)
		return
	}
	var secret []byte
	if found {
		// A resource with no webhook of its own returns no secret, so the check below rejects it the same
		// way a wrong signature is rejected: there is nothing here that could have signed this post.
		if secret, _, err = g.WebhookSecret(r.Context(), g.DB, rid); err != nil {
			internalErr(w, err)
			return
		}
	}
	signed := found && len(secret) > 0 && git.VerifySignature(secret, raw, sig)
	// An App installed on an organisation signs every repository's deliveries with one secret and posts them all to
	// one URL, so a resource that has no webhook of its own is not proof that nothing signed this. The App's secret
	// is tried second, over the same exact bytes: the HMAC rule does not change, only which held secret may satisfy
	// it, and a panel without CODEPLO_GITHUB_APP_WEBHOOK_SECRET behaves exactly as it did.
	if !signed && found && s.GitHubApp != nil && len(s.GitHubApp.WebhookSecret) > 0 {
		signed = git.VerifySignature(s.GitHubApp.WebhookSecret, raw, sig)
	}
	if !signed { // A payload naming no configured repository is refused without a delivery row. That is a deliberate
		// difference from recording it: this endpoint is public, and every anonymous POST that writes a row turns
		// the delivery history into a disk-filling target.
		if found {
			reason := "Invalid HMAC-SHA256 signature"
			row := git.Delivery{Provider: "github", Event: event, Result: "rejected", Reason: &reason, ResourceID: &rid}
			if _, err := g.RecordDelivery(r.Context(), g.DB, row); err != nil {
				internalErr(w, err)
				return
			}
		}
		writeErr(w, 401, "unauthenticated", "Invalid signature", nil)
		return
	}
	if deliveryID == "" {
		invalid(w, map[string]string{"X-GitHub-Delivery": "Missing delivery id"})
		return
	}
	// A pull request delivery is not a push: it asks for an environment to review a branch in, not for the deployed
	// service to change. Everything above it is shared because all of it is what makes the post trustworthy — the
	// signature over the exact bytes, the repository resolving to a configured resource, a delivery id worth
	// deduping — and only the action differs.
	if event == "pull_request" {
		s.receivePreview(w, r, g, rid, src, event, deliveryID, payload)
		return
	}

	reason := git.EvaluatePush(event, src, p)
	if reason == "" {
		if busy, err := s.hasInProgress(r.Context(), rid); err != nil {
			internalErr(w, err)
			return
		} else if busy {
			reason = "A deployment is already in progress"
		}
	}
	result := "success"
	if reason != "" {
		result = "ignored"
	}
	row := git.Delivery{Provider: "github", DeliveryID: deliveryID, Event: event, Result: result, ResourceID: &rid}
	if reason != "" {
		row.Reason = &reason
	}
	if p.SHA != "" {
		row.CommitSHA = &p.SHA
	}

	depID := ""
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		claimed, err := g.ClaimDelivery(r.Context(), tx, row)
		if err != nil {
			return audit.Event{}, err
		}
		if !claimed {
			return audit.Event{}, errReplay
		}
		if reason != "" {
			return audit.Event{}, nil // the ignored delivery is the record; nothing else to do
		}
		// Mode build with no image reference: the engine derives codeplo/<slug>:<deployment> from the clone, and a
		// reference from the payload would be an image this host never built.
		depID, err = e.Create(r.Context(), tx, s.Jobs, deploy.NewDeployment{
			ResourceID: rid, Trigger: deploy.TrWebhook, Mode: "build", Branch: p.Branch,
		})
		if err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, nil, "deployment.created", "deployment", depID, map[string]any{
			"resource_id": rid, "trigger": "webhook", "branch": p.Branch, "commit": p.SHA,
		})
	})
	if errors.Is(txErr, errReplay) {
		s.deliveryReplay(w, r, g, event, rid, deliveryID)
		return
	}
	if txErr != nil {
		internalErr(w, txErr)
		return
	}
	if reason == "" {
		s.Bus.Publish("resource.updated", map[string]any{"resource_id": rid})
		s.Bus.Publish("deployment.updated", map[string]any{"deployment_id": depID, "resource_id": rid})
	}
	writeJSON(w, 202, map[string]any{"delivery_id": deliveryID, "result": result})
}

// deliveryReplay is the answer to a second post of a delivery id this panel has already worked: a row saying so under
// a fresh id — the unique constraint is on the caller's id, and a replay has to be visible in the history rather than
// silently absent — and a 409, which tells GitHub the delivery is done with.
func (s *Server) deliveryReplay(w http.ResponseWriter, r *http.Request, g *git.Service, event, rid, deliveryID string) {
	replay := "Delivery ID already seen (replay)"
	dup := git.Delivery{Provider: "github", DeliveryID: deliveryID + "#replay" + crypto.RandomHex(2),
		Event: event, Result: "rejected", Reason: &replay, ResourceID: &rid}
	if _, err := g.RecordDelivery(r.Context(), g.DB, dup); err != nil {
		internalErr(w, err)
		return
	}
	writeErr(w, 409, "conflict", "Delivery already processed", nil)
}

// receivePreview acts on one authenticated pull_request delivery: it builds or refreshes the preview the request asks
// for, takes one down when the request stops asking, and records which of those happened.
//
// The delivery row is the transaction's last statement rather than its first, which is the one difference from the
// push path. There the reason is known before anything is written, so the row can be claimed up front; here the
// answer is what the preview service decides — whether a hostname was free, whether the parent is previewable,
// whether one was running at all — and a row that said "success" about a refusal would be the silence this endpoint
// exists to avoid. Claiming last costs nothing on that rule: SQLite takes the write lock at BEGIN, so a second post
// of the same id waits for the first to commit, then fails on the unique index and unwinds the preview work it had
// done along with everything else in its transaction.
func (s *Server) receivePreview(w http.ResponseWriter, r *http.Request, g *git.Service, rid string,
	src git.Source, event, deliveryID string, payload map[string]any) {
	pr, refusal := providers.ParsePullRequest(payload)
	action, reason := git.PRIgnore, refusal
	if refusal == "" {
		action, reason = git.DecidePullRequest(event, src, pr)
	}
	if action != git.PRIgnore && s.Preview == nil {
		action, reason = git.PRIgnore, "Preview environments are not available on this daemon"
	}
	result := "ignored"
	if reason == "" {
		result = "success"
	}
	row := git.Delivery{Provider: "github", DeliveryID: deliveryID, Event: event, Result: result, ResourceID: &rid}
	if reason != "" {
		row.Reason = &reason
	}
	if len(pr.HeadSHA) >= 7 {
		// The short form, exactly like a push delivery records it: the history is read by eye, and the full sha is
		// on the preview row for anyone who needs the commit itself.
		sha := pr.HeadSHA[:7]
		row.CommitSHA = &sha
	}

	previewed := "" // the preview's own resource, new or refreshed or just taken down
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		switch action {
		case git.PRBuild:
			prev, why, err := s.Preview.Build(r.Context(), tx, previews.Request{ParentResourceID: rid, PR: pr})
			if err != nil {
				return audit.Event{}, err
			}
			if why != "" {
				// A refusal is reached before the service writes anything, so the row can say so truthfully and the
				// transaction still has nothing in it but the claim.
				reason, result = why, "ignored"
				row.Reason, row.Result = &reason, result
				break
			}
			previewed = prev.ResourceID
		case git.PRTeardown:
			prev, found, err := s.Preview.Close(r.Context(), tx, rid, pr.Number, git.PullRequestNote(pr))
			if err != nil {
				return audit.Event{}, err
			}
			if !found {
				// The ordinary case of a closed draft, a fork, or a request against another branch: every one of
				// those was refused at the gate and GitHub sends a closed delivery for it anyway. There is nothing to
				// take down, which is what the requester wanted, so it is recorded and answered with 202.
				reason, result = "No preview of this pull request is running here", "ignored"
				row.Reason, row.Result = &reason, result
				break
			}
			previewed = prev.ResourceID
		}
		claimed, err := g.ClaimDelivery(r.Context(), tx, row)
		if err != nil {
			return audit.Event{}, err
		}
		if !claimed {
			return audit.Event{}, errReplay
		}
		return audit.Event{}, nil
	})
	if errors.Is(txErr, errReplay) {
		s.deliveryReplay(w, r, g, event, rid, deliveryID)
		return
	}
	if txErr != nil {
		internalErr(w, txErr)
		return
	}
	if previewed != "" {
		// The preview is an ordinary resource with an ordinary route: the panel's lists refresh on its id, and the
		// edge is told because a hostname was added or taken away — on a refresh too, because a preview that failed
		// to get its certificate the first time is re-issued by the same reconcile.
		s.Bus.Publish("resource.updated", map[string]any{"resource_id": previewed})
		s.Bus.Publish("edge.changed", map[string]any{})
		s.reconcileAfter()
	}
	writeJSON(w, 202, map[string]any{"delivery_id": deliveryID, "result": result})
}

// receiveDeployHook lets a CI pipeline that built the image itself ask this host to run it. The token is the only
// secret and the image policy is the only boundary: a hook issued for ghcr.io/org/app:* cannot deploy anything
// else, which is what stops a leaked CI credential becoming an arbitrary-image pull.
func (s *Server) receiveDeployHook(w http.ResponseWriter, r *http.Request) {
	g, ok := s.gitSvc(w, r)
	if !ok {
		return
	}
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	h, rid, found, err := g.ResolveHook(r.Context(), g.DB, chi.URLParam(r, "token"))
	if err != nil {
		internalErr(w, err)
		return
	}
	if !found {
		// No delivery row for an unknown token, for the same reason as above: it names no resource, and guessing
		// at tokens is exactly the traffic that must not be able to write.
		writeErr(w, 404, "not_found", "Unknown deploy hook", nil)
		return
	}
	// Only `image` is read. A pipeline that wants an immutable deploy passes the digest itself as the reference
	// (ghcr.io/org/app@sha256:...), which the policy check accepts; a separate digest field would be a second
	// source of truth for the same image, and the two disagreeing is the failure that pulls the wrong bytes.
	var b struct {
		Image *string `json:"image"`
	}
	if !decode(w, r, &b) {
		return
	}
	image := ""
	if b.Image != nil {
		image = *b.Image
	}
	if !git.MatchImage(h.ImagePolicy, image) {
		shown := image
		if shown == "" {
			shown = "(none)"
		}
		reason := `Image "` + shown + `" is outside the hook's image policy ` + h.ImagePolicy
		row := git.Delivery{Provider: "deploy_hook", Event: "deploy", Result: "rejected", Reason: &reason, ResourceID: &rid}
		if _, err := g.RecordDelivery(r.Context(), g.DB, row); err != nil {
			internalErr(w, err)
			return
		}
		invalid(w, map[string]string{"image": "Image must match " + h.ImagePolicy})
		return
	}
	if busy, err := s.hasInProgress(r.Context(), rid); err != nil {
		internalErr(w, err)
		return
	} else if busy {
		writeErr(w, 409, "conflict", "A deployment is already in progress", nil)
		return
	}

	depID := ""
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := g.RecordDelivery(r.Context(), tx, git.Delivery{
			Provider: "deploy_hook", Event: "deploy", Result: "success", ResourceID: &rid,
		}); err != nil {
			return audit.Event{}, err
		}
		if err := g.MarkHookUsed(r.Context(), tx, h.ID); err != nil {
			return audit.Event{}, err
		}
		depID, err = e.Create(r.Context(), tx, s.Jobs, deploy.NewDeployment{
			ResourceID: rid, Trigger: deploy.TrDeployHook, Mode: "pull", ImageRef: image,
		})
		if err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, nil, "deployment.created", "deployment", depID, map[string]any{
			"resource_id": rid, "trigger": "deploy_hook", "image_ref": image,
		})
	})
	if txErr != nil {
		internalErr(w, txErr)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": rid})
	s.Bus.Publish("deployment.updated", map[string]any{"deployment_id": depID, "resource_id": rid})
	writeJSON(w, 202, map[string]any{"deployment_id": depID})
}
