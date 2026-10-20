package api

import "testing"

func TestTokenBucket(t *testing.T) {
	l := newLimiter()
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("k", 3, 0.001); !ok {
			t.Fatalf("request %d should pass", i)
		}
	}
	ok, wait := l.allow("k", 3, 0.001)
	if ok || wait < 1 {
		t.Fatalf("4th request must be limited with Retry-After, got ok=%v wait=%d", ok, wait)
	}
	if ok, _ := l.allow("other", 3, 0.001); !ok {
		t.Fatal("buckets are per key")
	}
}
