package git

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
)

// WebhookInfo is what the panel shows about a repository webhook: enough to configure GitHub with, and no secret.
type WebhookInfo struct {
	Configured   bool    `json:"configured"`
	SecretPrefix *string `json:"secret_prefix"`
	CreatedAt    *string `json:"created_at"`
	Path         string  `json:"path"`
}

// WebhookCreated is the once-only response that carries the secret.
type WebhookCreated struct {
	Webhook WebhookInfo `json:"webhook"`
	Secret  string      `json:"secret"`
}

// WebhookPath is the receiver's URL, fixed by the contract: the panel prints it and GitHub posts to it.
const WebhookPath = "/api/v1/webhooks/github"

func newWebhookSecret() string { return "whsec_" + crypto.RandomHex(20) }

// GetWebhook reports whether a resource has a webhook secret configured.
func (s *Service) GetWebhook(ctx context.Context, q Querier, resourceID string) (WebhookInfo, error) {
	var (
		prefix, created string
	)
	err := q.QueryRowContext(ctx, `SELECT secret_prefix, created_at FROM webhook_secrets WHERE resource_id=?`, resourceID).Scan(&prefix, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookInfo{Configured: false, SecretPrefix: nil, CreatedAt: nil, Path: WebhookPath}, nil
	}
	if err != nil {
		return WebhookInfo{}, err
	}
	return WebhookInfo{Configured: true, SecretPrefix: &prefix, CreatedAt: &created, Path: WebhookPath}, nil
}

// CreateWebhook issues (or re-issues) the HMAC secret. Re-issuing is allowed because the operator is the only one
// who can call it, and changing a secret on the panel side before the repository side is a normal way to recover
// from a leak.
func (s *Service) CreateWebhook(ctx context.Context, q Querier, resourceID string) (WebhookCreated, error) {
	secret := newWebhookSecret()
	blob, err := crypto.Seal(s.Key, []byte(secret))
	if err != nil {
		return WebhookCreated{}, err
	}
	created, prefix := s.ts(), secret[:tokenPrefixLen]
	_, err = q.ExecContext(ctx, `INSERT INTO webhook_secrets(resource_id,secret_enc,secret_prefix,created_at) VALUES (?,?,?,?)
		ON CONFLICT(resource_id) DO UPDATE SET secret_enc=excluded.secret_enc, secret_prefix=excluded.secret_prefix, created_at=excluded.created_at`,
		resourceID, blob, prefix, created)
	if err != nil {
		return WebhookCreated{}, err
	}
	return WebhookCreated{
		Webhook: WebhookInfo{Configured: true, SecretPrefix: &prefix, CreatedAt: &created, Path: WebhookPath},
		Secret:  secret,
	}, nil
}

// WebhookSecret returns the plaintext secret for a resource, or absent when it never configured one.
func (s *Service) WebhookSecret(ctx context.Context, q Querier, resourceID string) (secret []byte, absent bool, err error) {
	var blob []byte
	err = q.QueryRowContext(ctx, `SELECT secret_enc FROM webhook_secrets WHERE resource_id=?`, resourceID).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	secret, err = crypto.Open(s.Key, blob)
	if err != nil {
		return nil, false, ErrSealedKey
	}
	return secret, false, nil
}

// VerifySignature checks GitHub's X-Hub-Signature-256 over the exact bytes received. It compares through
// hmac.Equal, which is constant time: a timing oracle here would leak a secret that can deploy.
func VerifySignature(secret, raw []byte, header string) bool {
	const alg = "sha256="
	if !strings.HasPrefix(header, alg) {
		return false
	}
	want, err := hex.DecodeString(strings.TrimSpace(header[len(alg):]))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(raw)
	return hmac.Equal(mac.Sum(nil), want)
}

// Push is the part of a GitHub push event this panel acts on.
type Push struct {
	Repo       string   // the repository URL as GitHub reports it (ssh_url or clone_url)
	Branch     string   // ref without refs/heads/
	SHA        string   // short sha of the new head, empty when the payload has none
	Message    string   // head commit message
	Changed    []string // every path added, modified or removed by the push
	HasChanges bool     // false when the payload carried no commit list at all
}

// EvaluatePush applies the gate order the panel documents: event, branch, skip marker, path filters. It returns
// the reason a push did not deploy; empty reason means deploy it.
//
// The branch and filter checks are not politeness. Without them a push to any branch, or a README edit in a
// monorepo, redeploys the whole site.
func EvaluatePush(event string, src Source, p Push) string {
	if event != "push" {
		return "Event " + strconv.Quote(event) + " is not handled"
	}
	if src.Branch == nil || deref(src.Branch) != p.Branch {
		return "Branch " + strconv.Quote(p.Branch) + " does not match configured branch " + strconv.Quote(deref(src.Branch))
	}
	if SkipMarker(p.Message) {
		return "Commit message contains [skip deploy]"
	}
	if len(src.PathFilters) > 0 && p.HasChanges && !AnyPathMatches(src.PathFilters, p.Changed) {
		return "No changed path matches the configured path filters"
	}
	return ""
}

// SkipMarker is the opt-out every CI system honours, matched case-insensitively anywhere in the message.
func SkipMarker(message string) bool {
	return strings.Contains(strings.ToLower(message), "[skip deploy]")
}

// AnyPathMatches is the filter language: "app/**" matches everything under app/, any other entry is an exact path.
// A push with no commit list at all is not filtered (see Push.HasChanges) — refusing to deploy because a payload
// was truncated would be the wrong direction of failure.
func AnyPathMatches(filters, changed []string) bool {
	for _, p := range changed {
		for _, f := range filters {
			if strings.HasSuffix(f, "/**") {
				if strings.HasPrefix(p, strings.TrimSuffix(f, "**")) {
					return true
				}
				continue
			}
			if p == f {
				return true
			}
		}
	}
	return false
}

// Delivery is one received webhook or deploy-hook call, accepted or not.
type Delivery struct {
	ID         string  `json:"id"`
	Provider   string  `json:"provider"`
	DeliveryID string  `json:"delivery_id"`
	ReceivedAt string  `json:"received_at"`
	Event      string  `json:"event"`
	Result     string  `json:"result"`
	Reason     *string `json:"reason"`
	CommitSHA  *string `json:"commit_sha"`
	ResourceID *string `json:"resource_id"`
}

// ClaimDelivery writes the delivery row and reports whether this call is the first time the id has been seen.
// The UNIQUE(provider, delivery_id) constraint is the dedupe: two concurrent retries of the same delivery race in
// the index, and the loser gets false instead of both of them deploying.
func (s *Service) ClaimDelivery(ctx context.Context, q Querier, d Delivery) (bool, error) {
	err := s.insertDelivery(ctx, q, d)
	if err != nil {
		if isUniqueErr(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// RecordDelivery writes a delivery row that is not part of a replay decision (a rejection, an ignored event). The
// caller has already decided what the row says; a failure here is the database's, not the delivery's.
func (s *Service) RecordDelivery(ctx context.Context, q Querier, d Delivery) (Delivery, error) {
	d.ID, d.ReceivedAt = s.id("whd_"), s.ts()
	if d.Provider == "" {
		d.Provider = "github"
	}
	if d.DeliveryID == "" {
		d.DeliveryID = crypto.RandomHex(8)
	}
	return d, s.insertDelivery(ctx, q, d)
}

func (s *Service) insertDelivery(ctx context.Context, q Querier, d Delivery) error {
	if d.ID == "" {
		d.ID = s.id("whd_")
	}
	if d.ReceivedAt == "" {
		d.ReceivedAt = s.ts()
	}
	if d.Provider == "" {
		d.Provider = "github"
	}
	// reason is NOT NULL with an empty default: "no reason" is written as '', not as NULL, or the insert fails on
	// every accepted delivery.
	reason := ""
	if d.Reason != nil {
		reason = *d.Reason
	}
	_, err := q.ExecContext(ctx, `INSERT INTO webhook_deliveries(id,provider,delivery_id,received_at,event,result,reason,commit_sha,resource_id)
		VALUES (?,?,?,?,?,?,?,?,?)`, d.ID, d.Provider, d.DeliveryID, d.ReceivedAt, d.Event, d.Result,
		reason, d.CommitSHA, d.ResourceID)
	return err
}

// ListDeliveries pages the history newest-first. The cursor is the last id seen; ids are ULIDs, so id order is
// time order and the page cannot shift when a delivery lands mid-read.
func (s *Service) ListDeliveries(ctx context.Context, q Querier, resourceID string, limit int, cursor string) ([]Delivery, string, error) {
	where, args := `1=1`, []any{}
	if resourceID != "" {
		where, args = `resource_id=?`, append(args, resourceID)
	}
	if cursor != "" {
		where += ` AND id<?`
		args = append(args, cursor)
	}
	args = append(args, limit+1)
	rows, err := q.QueryContext(ctx, `SELECT id, provider, delivery_id, received_at, event, result, reason, commit_sha, resource_id
		FROM webhook_deliveries WHERE `+where+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []Delivery{}
	for rows.Next() {
		var (
			d                Delivery
			reason, sha, rid sql.NullString
		)
		if err := rows.Scan(&d.ID, &d.Provider, &d.DeliveryID, &d.ReceivedAt, &d.Event, &d.Result, &reason, &sha, &rid); err != nil {
			return nil, "", err
		}
		d.Reason, d.CommitSHA, d.ResourceID = str(reason), str(sha), str(rid)
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		next = items[limit-1].ID
		items = items[:limit]
	}
	return items, next, nil
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// PruneDeliveries removes webhook_deliveries older than logs.retention_days (default 14).
// T37: delivery history is append-only without this pass and would grow without bound on a busy panel.
func (s *Service) PruneDeliveries(ctx context.Context) (int, error) {
	days := 14
	var v string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='logs.retention_days'`).Scan(&v); err == nil {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	cutoff := s.now().UTC().Add(-24 * time.Duration(days) * time.Hour).Format("2006-01-02T15:04:05.000Z")
	res, err := s.DB.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE received_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
