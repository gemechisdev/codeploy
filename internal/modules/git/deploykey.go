package git

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/codeplo/codeplo/internal/crypto"
)

// DeployKey is what the panel shows for a resource's key: the line to paste into the repository's deploy-key
// settings plus its fingerprint to recognise it later. There is no field for the private half, and adding one
// would be the bug this whole file exists to avoid.
type DeployKey struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"created_at"`
}

// GenerateDeployKey makes an ed25519 key pair for a resource, stores the public half in plain view and seals the
// private half into resource_credentials. A resource has one key: regenerating replaces it, which is why the
// operator is told to update the repository setting (the mock's UI does the same, and a second key on the same
// resource would leave no way to say which one the source uses).
func (s *Service) GenerateDeployKey(ctx context.Context, q Querier, resourceID, resourceName string) (DeployKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return DeployKey{}, err
	}
	k := DeployKey{
		ID:          s.id("key_"),
		Title:       resourceName + " deploy key",
		PublicKey:   authorizedKey(pub, "codeplo:"+resourceName),
		Fingerprint: fingerprint(pub),
		CreatedAt:   s.ts(),
	}
	keyPEM, err := privatePEM(priv, k.Title)
	if err != nil {
		return DeployKey{}, err
	}
	blob, err := crypto.Seal(s.Key, keyPEM)
	if err != nil {
		return DeployKey{}, err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM deploy_keys WHERE resource_id=?`, resourceID); err != nil {
		return DeployKey{}, err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO deploy_keys(id,resource_id,title,public_key,fingerprint,created_at) VALUES (?,?,?,?,?,?)`,
		k.ID, resourceID, k.Title, k.PublicKey, k.Fingerprint, k.CreatedAt); err != nil {
		return DeployKey{}, err
	}
	if err := s.SealCredential(ctx, q, resourceID, KindDeployKeyCred, blob); err != nil {
		return DeployKey{}, err
	}
	return k, nil
}

// GetDeployKey returns the stored public half.
func (s *Service) GetDeployKey(ctx context.Context, q Querier, resourceID string) (DeployKey, error) {
	var k DeployKey
	err := q.QueryRowContext(ctx, `SELECT id, title, public_key, fingerprint, created_at FROM deploy_keys WHERE resource_id=?`, resourceID).
		Scan(&k.ID, &k.Title, &k.PublicKey, &k.Fingerprint, &k.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeployKey{}, NotFound{What: "deploy key"}
	}
	return k, err
}

// authorizedKey renders the OpenSSH one-line form, which is what every git host asks for.
func authorizedKey(pub ed25519.PublicKey, comment string) string {
	p, err := ssh.NewPublicKey(pub)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(ssh.MarshalAuthorizedKey(p)), "\n") + " " + comment
}

func fingerprint(pub ed25519.PublicKey) string {
	p, err := ssh.NewPublicKey(pub)
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(p)
}

// privatePEM encodes the signing key in OpenSSH's own format, so the clone step can hand it to ssh without a
// conversion. Unencrypted: the master key seals it at rest, and a passphrase would only move the problem into the
// daemon, which has nobody to ask for it.
func privatePEM(priv ed25519.PrivateKey, comment string) ([]byte, error) {
	blk, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(blk), nil
}

// SealCredential writes one encrypted blob for a resource, replacing any previous value of the same kind.
func (s *Service) SealCredential(ctx context.Context, q Querier, resourceID, kind string, blob []byte) error {
	_, err := q.ExecContext(ctx, `INSERT INTO resource_credentials(resource_id,kind,blob_enc,updated_at) VALUES (?,?,?,?)
		ON CONFLICT(resource_id,kind) DO UPDATE SET blob_enc=excluded.blob_enc, updated_at=excluded.updated_at`,
		resourceID, kind, blob, s.ts())
	return err
}

// OpenCredential decrypts a resource credential. absent is false only when a row exists; a row that will not open
// is ErrSealedKey, never a silent nil, because "no key" and "wrong master key" need different answers.
func (s *Service) OpenCredential(ctx context.Context, q Querier, resourceID, kind string) (plain []byte, absent bool, err error) {
	var blob []byte
	err = q.QueryRowContext(ctx, `SELECT blob_enc FROM resource_credentials WHERE resource_id=? AND kind=?`, resourceID, kind).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	plain, err = crypto.Open(s.Key, blob)
	if err != nil {
		return nil, false, ErrSealedKey
	}
	return plain, false, nil
}
