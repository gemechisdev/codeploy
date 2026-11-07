package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// feCall matches the SPA's typed client calls: api.GET('/projects/{id}', ...).
var feCall = regexp.MustCompile("api\\.(GET|POST|PUT|PATCH|DELETE)\\(\\s*['\"`]([^'\"`]+)['\"`]")

// TestFrontendRoutesAreRegistered fails when the SPA calls a method+path that Server.Routes() does not register.
// A missing route surfaces to users as a hard 404 "no such endpoint"; a not-yet-built feature must be a registered
// 501 stub instead (laterPhase), so the UI can say "coming soon".
func TestFrontendRoutesAreRegistered(t *testing.T) {
	root := filepath.Join("..", "..", "web", "src", "api")
	if _, err := os.Stat(root); err != nil {
		t.Skip("web/src/api not present")
	}
	e := newEnv(t)
	have := map[string]bool{}
	for _, d := range e.s.Routes() {
		have[d.Method+" "+d.Path] = true
	}
	var missing []string
	seen := map[string]bool{}
	// useControlResource builds its path from a template literal (`/resources/{id}/${action}`), which the matcher
	// cannot see, so its three actions are listed here.
	for _, a := range []string{"start", "stop", "restart"} {
		k := "POST /resources/{id}/" + a
		seen[k] = true
		if !have[k] {
			missing = append(missing, k+"  (workloads.ts)")
		}
	}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ts") && !strings.HasSuffix(p, ".tsx") || strings.Contains(p, "__tests__") || strings.HasSuffix(p, "schema.d.ts") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range feCall.FindAllStringSubmatch(string(b), -1) {
			k := m[1] + " " + m[2]
			if seen[k] {
				continue
			}
			seen[k] = true
			if !have[k] {
				missing = append(missing, k+"  ("+filepath.Base(p)+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("found no frontend api calls; the matcher is stale")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("frontend calls routes the backend does not register (would 404):\n  %s", strings.Join(missing, "\n  "))
	}
}
