package config

import "testing"

func TestValidateRefusesPublicBind(t *testing.T) {
	for addr, ok := range map[string]bool{"127.0.0.1:7443": true, "[::1]:7443": true, "0.0.0.0:7443": false, "10.0.0.5:7443": false, "example.com:80": false, "7443": false} {
		err := Config{Listen: addr}.Validate()
		if (err == nil) != ok {
			t.Errorf("%s: ok=%v err=%v", addr, ok, err)
		}
	}
}
