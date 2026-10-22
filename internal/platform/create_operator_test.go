package platform

import "testing"

func TestValidateSSHPublicKey(t *testing.T) {
	cases := []struct {
		key string
		ok  bool
	}{
		{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest comment", true},
		{"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ", true},
		{"", false},
		{"ssh-ed25519", false},
		{"not-a-key foo", false},
		{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest\nsecond", false},
	}
	for _, c := range cases {
		err := validateSSHPublicKey(c.key)
		if c.ok && err != nil {
			t.Errorf("key %q: unexpected err %v", c.key, err)
		}
		if !c.ok && err == nil {
			t.Errorf("key %q: want error", c.key)
		}
	}
}
