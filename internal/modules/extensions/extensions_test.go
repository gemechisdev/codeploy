package extensions

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/store"
)

func testSvc(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return New(db, key)
}

func TestRegisterEnqueueDeliver(t *testing.T) {
	svc := testSvc(t)
	ctx := context.Background()
	// enable outbound
	if _, err := svc.DB.Exec(`UPDATE feature_flags SET enabled=1 WHERE key='extensions.outbound'`); err != nil {
		t.Fatal(err)
	}
	ext, secret, err := svc.Register(ctx, "sample", "http://127.0.0.1:9/hook", []string{"deployment.succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || ext.ID == "" {
		t.Fatal("missing secret or id")
	}
	var hits int
	svc.HTTPDo = func(ctx context.Context, url string, headers map[string]string, body []byte) (int, error) {
		hits++
		if headers["X-Codeplo-Signature-256"] == "" {
			t.Error("missing signature")
		}
		return 200, nil
	}
	n, err := svc.Enqueue(ctx, "deployment.succeeded", map[string]string{"id": "dep_1"})
	if err != nil || n != 1 {
		t.Fatalf("enqueue: n=%d err=%v", n, err)
	}
	sent, err := svc.DeliverDue(ctx, 10)
	if err != nil || sent != 1 || hits != 1 {
		t.Fatalf("deliver: sent=%d hits=%d err=%v", sent, hits, err)
	}
}

func TestAuthorizeAction(t *testing.T) {
	if _, err := AuthorizeAction("deploy.trigger", "deploy.trigger"); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeAction("resources.list", "deploy.trigger"); err == nil {
		t.Fatal("expected scope failure")
	}
	_ = crypto.RandomHex(2)
}
