package git

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	"github.com/codeplo/codeplo/internal/crypto"
)

// Hook is a resource's deploy-hook metadata. It never carries the token: the plaintext exists only in the create
// or rotate response, and the column here holds its SHA-256.
type Hook struct {
	ID          string  `json:"id"`
	TokenPrefix string  `json:"token_prefix"`
	LastUsedAt  *string `json:"last_used_at"`
	CreatedAt   string  `json:"created_at"`
	ImagePolicy string  `json:"image_policy"`
}

// HookCreated is the once-only response: the metadata to show in the panel plus the secret to paste into CI.
type HookCreated struct {
	Hook  Hook   `json:"hook"`
	Token string `json:"token"`
}

// tokenPrefixLen is how much of a token is safe to display. The prefix is "cplh_" plus four hex characters, which
// identifies a secret in a log without being anywhere near enough to use it.
const tokenPrefixLen = 9

var imageRefRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]{0,200}(@sha256:[a-f0-9]{64})?$`)

// PolicyFor turns the resource's configured image (or the registry location the panel suggests when it has none)
// into the tag space the hook may deploy. Truncating at the first ":" or "@" keeps the repository and drops the
// tag or digest the resource currently runs.
func PolicyFor(imageRef, slug string) string {
	base := strings.TrimSpace(imageRef)
	if base == "" {
		base = "ghcr.io/org/" + slug
	}
	if i := strings.IndexAny(base, ":@"); i >= 0 {
		base = base[:i]
	}
	return base + ":*"
}

// newToken makes a hook token. 24 hex characters is 96 bits, which is enough that neither the rate limit nor the
// 404-on-unknown-token has to carry the security of the endpoint.
func newToken() string { return "cplh_" + crypto.RandomHex(24) }

func (s *Service) GetHook(ctx context.Context, q Querier, resourceID string) (Hook, error) {
	var (
		h  Hook
		la sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT id, token_prefix, image_policy, created_at, last_used_at FROM deploy_hooks WHERE resource_id=?`, resourceID).
		Scan(&h.ID, &h.TokenPrefix, &h.ImagePolicy, &h.CreatedAt, &la)
	if errors.Is(err, sql.ErrNoRows) {
		return Hook{}, NotFound{What: "deploy hook"}
	}
	if err != nil {
		return Hook{}, err
	}
	h.LastUsedAt = str(la)
	return h, nil
}

// CreateHook issues the first token for a resource. It refuses when one exists rather than silently invalidating
// the CI job that still holds the old secret; rotation is the explicit route for that.
func (s *Service) CreateHook(ctx context.Context, q Querier, resourceID, imageRef, slug string) (HookCreated, error) {
	if _, err := s.GetHook(ctx, q, resourceID); err == nil {
		return HookCreated{}, Conflict{Msg: "A deploy hook already exists. Rotate it to get a new token."}
	} else {
		var nf NotFound
		if !errors.As(err, &nf) {
			return HookCreated{}, err
		}
	}
	return s.issueHook(ctx, q, resourceID, s.id("hok_"), imageRef, slug)
}

// RotateHook replaces the token, keeping the hook's identity so history and any CI reference to the resource stay
// meaningful. The old token stops working the moment this commits.
func (s *Service) RotateHook(ctx context.Context, q Querier, resourceID, imageRef, slug string) (HookCreated, error) {
	prev, err := s.GetHook(ctx, q, resourceID)
	if err != nil {
		return HookCreated{}, err
	}
	return s.issueHook(ctx, q, resourceID, prev.ID, imageRef, slug)
}

func (s *Service) issueHook(ctx context.Context, q Querier, resourceID, hookID, imageRef, slug string) (HookCreated, error) {
	token := newToken()
	h := Hook{
		ID: hookID, TokenPrefix: token[:tokenPrefixLen], CreatedAt: s.ts(),
		ImagePolicy: PolicyFor(imageRef, slug),
	}
	_, err := q.ExecContext(ctx, `INSERT INTO deploy_hooks(id,resource_id,token_hash,token_prefix,image_policy,created_at,last_used_at)
		VALUES (?,?,?,?,?,?,NULL)
		ON CONFLICT(resource_id) DO UPDATE SET token_hash=excluded.token_hash, token_prefix=excluded.token_prefix,
			image_policy=excluded.image_policy, created_at=excluded.created_at, last_used_at=NULL`,
		h.ID, resourceID, crypto.HashToken(token), h.TokenPrefix, h.ImagePolicy, h.CreatedAt)
	if err != nil {
		return HookCreated{}, err
	}
	return HookCreated{Hook: h, Token: token}, nil
}

// ResolveHook turns a presented token into its hook. The lookup is on the hash, so the comparison happens in the
// index rather than in a constant-time loop over every resource's secret, and a database read cannot be replayed
// as a deploy.
func (s *Service) ResolveHook(ctx context.Context, q Querier, token string) (Hook, string, bool, error) {
	var (
		h          Hook
		resourceID string
		la         sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT id, resource_id, token_prefix, image_policy, created_at, last_used_at FROM deploy_hooks WHERE token_hash=?`, crypto.HashToken(token)).
		Scan(&h.ID, &resourceID, &h.TokenPrefix, &h.ImagePolicy, &h.CreatedAt, &la)
	if errors.Is(err, sql.ErrNoRows) {
		return Hook{}, "", false, nil
	}
	if err != nil {
		return Hook{}, "", false, err
	}
	h.LastUsedAt = str(la)
	return h, resourceID, true, nil
}

// MarkHookUsed records the last successful deploy through this hook.
func (s *Service) MarkHookUsed(ctx context.Context, q Querier, hookID string) error {
	_, err := q.ExecContext(ctx, `UPDATE deploy_hooks SET last_used_at=? WHERE id=?`, s.ts(), hookID)
	return err
}

// MatchImage is the hook's tag policy. The repository boundary is the colon, not a string prefix: matching
// "ghcr.io/org/app:*" against "ghcr.io/org/app-evil:latest" would otherwise let a sibling repository deploy
// through a key issued for this one.
func MatchImage(policy, image string) bool {
	image = strings.TrimSpace(image)
	if image == "" || !strings.HasSuffix(policy, ":*") || !imageRefRe.MatchString(image) {
		return false
	}
	repo := policy[:len(policy)-2]
	if !strings.HasPrefix(image, repo+":") {
		return false
	}
	tag := image[len(repo)+1:]
	if tag == "" || strings.Contains(tag, "/") {
		return false
	}
	if i := strings.Index(tag, "@"); i >= 0 {
		tag = tag[:i] // an optional digest suffix; the tag part is what the policy constrains
	}
	return !strings.Contains(tag, ":")
}
