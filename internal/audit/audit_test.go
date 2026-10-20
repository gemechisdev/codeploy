package audit

import "testing"

func TestCanonicalSortsKeysAndSkipsHTMLEscape(t *testing.T) {
	got := Canonical(map[string]any{"b": 1, "a": "<x&y>", "c": map[string]any{"z": true, "y": nil}})
	want := `{"a":"<x&y>","b":1,"c":{"y":null,"z":true}}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if Canonical(nil) != "{}" {
		t.Fatal("nil meta must canonicalise to {}")
	}
}

// Same input formula as web/src/mock/db.ts recomputeHash: prev+seq+at+actor+action+target_type:target_id+canonical(meta).
func TestComputeHashDependsOnEveryField(t *testing.T) {
	base := ComputeHash(Genesis, 1, "2026-01-01T00:00:00.000Z", "usr_1", "auth.login", "user", "usr_1", "{}")
	if len(base) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(base))
	}
	variants := []string{
		ComputeHash("1"+Genesis[1:], 1, "2026-01-01T00:00:00.000Z", "usr_1", "auth.login", "user", "usr_1", "{}"),
		ComputeHash(Genesis, 2, "2026-01-01T00:00:00.000Z", "usr_1", "auth.login", "user", "usr_1", "{}"),
		ComputeHash(Genesis, 1, "2026-01-01T00:00:00.001Z", "usr_1", "auth.login", "user", "usr_1", "{}"),
		ComputeHash(Genesis, 1, "2026-01-01T00:00:00.000Z", "usr_2", "auth.login", "user", "usr_1", "{}"),
		ComputeHash(Genesis, 1, "2026-01-01T00:00:00.000Z", "usr_1", "auth.logout", "user", "usr_1", "{}"),
		ComputeHash(Genesis, 1, "2026-01-01T00:00:00.000Z", "usr_1", "auth.login", "token", "usr_1", "{}"),
		ComputeHash(Genesis, 1, "2026-01-01T00:00:00.000Z", "usr_1", "auth.login", "user", "usr_2", "{}"),
		ComputeHash(Genesis, 1, "2026-01-01T00:00:00.000Z", "usr_1", "auth.login", "user", "usr_1", `{"a":1}`),
	}
	for i, v := range variants {
		if v == base {
			t.Errorf("variant %d did not change the hash", i)
		}
	}
}
