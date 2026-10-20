package edge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderDeterministicAndOrderIndependent(t *testing.T) {
	o := Options{AdminSocket: "/run/codeplo-caddy/admin.sock", PanelSocket: "/var/lib/codeplo/codeplo.sock"}
	a := []Site{{Hostname: "b.example.com", Upstream: "app:3000", ForceHTTPS: true}, {Hostname: "panel.example.com", Panel: true}}
	b := []Site{a[1], a[0]}
	j1, h1, err := Render(a, o)
	if err != nil {
		t.Fatal(err)
	}
	j2, h2, _ := Render(b, o)
	if h1 != h2 || string(j1) != string(j2) {
		t.Fatal("render must not depend on input order")
	}
	var v map[string]any
	if json.Unmarshal(j1, &v) != nil {
		t.Fatal("output is not valid JSON")
	}
}

func TestRenderPanelHasHeadersAndAlwaysRedirects(t *testing.T) {
	o := Options{AdminSocket: "/a.sock", PanelSocket: "/p.sock"}
	j, _, _ := Render([]Site{{Hostname: "panel.example.com", Panel: true}}, o)
	s := string(j)
	for _, want := range []string{"Strict-Transport-Security", "Content-Security-Policy", `"dial":"unix//p.sock"`, "static_response"} {
		if !strings.Contains(s, want) {
			t.Errorf("panel config missing %q", want)
		}
	}
}

func TestRenderWWWRedirectAndPlainHTTP(t *testing.T) {
	o := Options{AdminSocket: "/a.sock", PanelSocket: "/p.sock"}
	j, _, _ := Render([]Site{{Hostname: "x.example.com", Upstream: "u:1", RedirectWWW: true}}, o)
	s := string(j)
	if !strings.Contains(s, "www.x.example.com") {
		t.Error("www redirect route missing")
	}
	if !strings.Contains(s, `"listen":["unix//a.sock|0660"]`) && !strings.Contains(s, "unix//a.sock|0660") {
		t.Error("admin socket not set")
	}
}
