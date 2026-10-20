package config

import (
	"os"
	"testing"
)

func TestValidateRefusesPublicBind(t *testing.T) {
	for addr, ok := range map[string]bool{"127.0.0.1:7443": true, "[::1]:7443": true, "0.0.0.0:7443": false, "10.0.0.5:7443": false, "example.com:80": false, "7443": false} {
		err := Config{Listen: addr}.Validate()
		if (err == nil) != ok {
			t.Errorf("%s: ok=%v err=%v", addr, ok, err)
		}
	}
}

// The loopback listener is mandatory: there is no config path that yields a Config without a
// valid loopback Listen (the daemon has no "disable loopback" knob to set).
func TestLoadYieldsOnlyLoopbackOrFails(t *testing.T) {
	os.Unsetenv("CODEPLO_LISTEN")
	c, err := Load()
	if err != nil || c.Listen != "127.0.0.1:7443" {
		t.Fatalf("default config must bind the loopback listener, got %q err=%v", c.Listen, err)
	}
	for _, bad := range []string{"0.0.0.0:7443", "192.168.1.10:7443", "external.example.com:7443"} {
		os.Setenv("CODEPLO_LISTEN", bad)
		if _, err := Load(); err == nil {
			t.Fatalf("%s must be refused, not loaded", bad)
		}
	}
	os.Unsetenv("CODEPLO_LISTEN")
}
