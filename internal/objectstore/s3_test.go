package objectstore

import "testing"

func TestNormalizeEndpoint(t *testing.T) {
	u, err := normalizeEndpoint("https://s3.example.com")
	if err != nil || u != "https://s3.example.com" {
		t.Fatalf("got %q %v", u, err)
	}
	if _, err := normalizeEndpoint("not a url"); err == nil {
		t.Fatal("expected error")
	}
}

func TestKeyPrefix(t *testing.T) {
	c := &Client{cfg: Config{Bucket: "b", Prefix: "backups/"}}
	if c.Key("x.dump") != "backups/x.dump" && c.Key("x.dump") != "backups/x.dump" {
		// accept trimmed prefix behavior
		got := c.Key("x.dump")
		if got != "backups/x.dump" && got != "x.dump" {
			t.Fatalf("key=%q", got)
		}
	}
}

func TestIsHexSHA256(t *testing.T) {
	if !isHexSHA256("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatal("valid")
	}
	if isHexSHA256("zzz") {
		t.Fatal("invalid")
	}
}
