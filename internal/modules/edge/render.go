// Package edge: managed Caddy (plan P2a). The renderer is pure: the same domain set always yields
// byte-identical JSON, so the SHA-256 of the output is a stable drift/reconcile key.
package edge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// Site is one routable hostname.
type Site struct {
	Hostname    string
	Panel       bool
	Upstream    string // host:port for resource sites; ignored for the panel site
	ForceHTTPS  bool
	RedirectWWW bool
}

// Options are the host-level inputs of the renderer.
type Options struct {
	AdminSocket string // Caddy admin API unix socket path
	PanelSocket string // codeplod unix socket (the panel upstream)
	ACMEEmail   string // optional
}

// panelHeaders are the security headers the panel domain always carries (plan C4).
var panelHeaders = map[string][]string{
	"Strict-Transport-Security": {"max-age=31536000"},
	"X-Content-Type-Options":    {"nosniff"},
	"X-Frame-Options":           {"DENY"},
	"Referrer-Policy":           {"same-origin"},
	"Content-Security-Policy":   {"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'"},
}

func hostMatch(hosts ...string) []any { return []any{map[string]any{"host": hosts}} }

func redirectRoute(hosts []string, target string) map[string]any {
	return map[string]any{
		"match": hostMatch(hosts...),
		"handle": []any{map[string]any{"handler": "static_response", "status_code": 308,
			"headers": map[string]any{"Location": []string{target}}}},
		"terminal": true,
	}
}

func proxyHandle(s Site, o Options) []any {
	dial := s.Upstream
	var hs []any
	if s.Panel {
		dial = "unix/" + o.PanelSocket
		hs = append(hs, map[string]any{"handler": "headers", "response": map[string]any{"set": panelHeaders}})
	}
	return append(hs, map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": dial}}})
}

// Render builds the complete Caddy JSON config. Redirects are explicit (automatic redirects are disabled)
// so per-site force_https is honoured; TLS is automatic for every hostname that appears in a host matcher.
func Render(sites []Site, o Options) ([]byte, string, error) {
	ss := append([]Site(nil), sites...)
	sort.Slice(ss, func(i, j int) bool { return ss[i].Hostname < ss[j].Hostname })
	httpsRoutes, httpRoutes := []any{}, []any{}
	for _, s := range ss {
		h := strings.ToLower(s.Hostname)
		if s.RedirectWWW && !strings.HasPrefix(h, "www.") {
			w := "www." + h
			httpsRoutes = append(httpsRoutes, redirectRoute([]string{w}, "https://"+h+"{http.request.uri}"))
			httpRoutes = append(httpRoutes, redirectRoute([]string{w}, "https://"+h+"{http.request.uri}"))
		}
		httpsRoutes = append(httpsRoutes, map[string]any{"match": hostMatch(h), "handle": proxyHandle(s, o), "terminal": true})
		if s.ForceHTTPS || s.Panel {
			httpRoutes = append(httpRoutes, redirectRoute([]string{h}, "https://{http.request.host}{http.request.uri}"))
		} else {
			httpRoutes = append(httpRoutes, map[string]any{"match": hostMatch(h), "handle": proxyHandle(s, o), "terminal": true})
		}
	}
	noRedirects := map[string]any{"disable_redirects": true}
	cfg := map[string]any{
		"admin": map[string]any{"listen": "unix/" + o.AdminSocket + "|0660"},
		"apps": map[string]any{
			"http": map[string]any{"servers": map[string]any{
				"https": map[string]any{"listen": []string{":443"}, "routes": httpsRoutes, "automatic_https": noRedirects},
				"http":  map[string]any{"listen": []string{":80"}, "routes": httpRoutes, "automatic_https": noRedirects},
			}},
		},
	}
	if o.ACMEEmail != "" {
		cfg["apps"].(map[string]any)["tls"] = map[string]any{"automation": map[string]any{"policies": []any{
			map[string]any{"issuers": []any{map[string]any{"module": "acme", "email": o.ACMEEmail}}}}}}
	}
	b, err := json.Marshal(cfg) // map keys are sorted by encoding/json: deterministic
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}
