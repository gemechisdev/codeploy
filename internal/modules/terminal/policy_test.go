package terminal

import (
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (pw, gr, flag string) {
	d := t.TempDir()
	pw, gr, flag = filepath.Join(d, "passwd"), filepath.Join(d, "group"), filepath.Join(d, "allow-root")
	os.WriteFile(pw, []byte("root:x:0:0::/root:/bin/bash\nops:x:1000:1000::/home/ops:/bin/bash\nweb:x:1001:1001::/home/web:/bin/bash\nsvc:x:1002:1002::/nonexistent:/usr/sbin/nologin\n"), 0o600)
	os.WriteFile(gr, []byte("sudo:x:27:ops\n"), 0o600)
	return
}

func TestAuthorizeOperatorOnly(t *testing.T) {
	pw, gr, flag := fixture(t)
	if a, err := Authorize(HelperRequest{User: "ops"}, pw, gr, flag); err != nil || a.UID != 1000 || a.Home != "/home/ops" {
		t.Fatalf("sudo user must be allowed: %+v %v", a, err)
	}
	for _, u := range []string{"web", "svc", "root", "nobody", "Ops", "ops;id", ""} {
		if _, err := Authorize(HelperRequest{User: u}, pw, gr, flag); err == nil {
			t.Errorf("user %q must be refused", u)
		}
	}
}

func TestAuthorizeRootNeedsHostFlag(t *testing.T) {
	pw, gr, flag := fixture(t)
	if _, err := Authorize(HelperRequest{Root: true}, pw, gr, flag); err != ErrRootDisabled {
		t.Fatalf("root without the host flag file must be refused, got %v", err)
	}
	os.WriteFile(flag, nil, 0o600)
	if a, err := Authorize(HelperRequest{Root: true}, pw, gr, flag); err != nil || a.UID != 0 {
		t.Fatalf("root with the flag file: %+v %v", a, err)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytesBuffer
	WriteFrame(&buf, FrameData, []byte("ls\n"))
	WriteFrame(&buf, FrameResize, ResizePayload(120, 40))
	WriteFrame(&buf, FrameExit, ExitPayload(-1))
	r := newReader(&buf)
	if typ, p, _ := ReadFrame(r); typ != FrameData || string(p) != "ls\n" {
		t.Fatal("data frame")
	}
	typ, p, _ := ReadFrame(r)
	if c, rw, err := ParseResize(p); typ != FrameResize || err != nil || c != 120 || rw != 40 {
		t.Fatal("resize frame")
	}
	typ, p, _ = ReadFrame(r)
	if code, err := ParseExit(p); typ != FrameExit || err != nil || code != -1 {
		t.Fatalf("exit frame: %d %v", code, err)
	}
}

func TestFrameRejectsOversize(t *testing.T) {
	if err := WriteFrame(&bytesBuffer{}, FrameData, make([]byte, maxFrame+1)); err == nil {
		t.Fatal("oversize frame must be refused")
	}
}

func TestClampSize(t *testing.T) {
	if c, r := ClampSize(0, 0); c != 80 || r != 24 {
		t.Fatal("defaults")
	}
	if c, r := ClampSize(100000, 100000); c != 80 || r != 24 {
		t.Fatal("absurd sizes fall back")
	}
	if c, r := ClampSize(132, 43); c != 132 || r != 43 {
		t.Fatal("sane sizes pass")
	}
}
