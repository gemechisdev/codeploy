// Package authz_test: the Go router must equal the OpenAPI contract — no extra route, none missing,
// and each route's declared permission must equal its x-permission.
package authz_test

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/codeplo/codeplo/internal/api"
)

// openapiPaths is parsed just enough to extract the (method, path, x-permission) triples.
type op struct {
	Permission *string `yaml:"x-permission"`
}

func openapiRoutes(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]op `yaml:"paths"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	set := map[string]string{}
	for path, ops := range doc.Paths {
		for m, o := range ops {
			m = strings.ToUpper(m)
			if m != "GET" && m != "POST" && m != "PUT" && m != "PATCH" && m != "DELETE" {
				continue
			}
			if o.Permission == nil {
				t.Fatalf("%s %s: missing x-permission", m, path)
			}
			set[m+" "+path] = *o.Permission
		}
	}
	return set
}

func TestRouterMatchesOpenAPI(t *testing.T) {
	srv := api.New(nil, make([]byte, 32), "test", "test", "test")
	goSet := map[string]string{}
	for _, r := range srv.Routes() {
		goSet[r.Method+" "+r.Path] = r.Perm
	}
	oaSet := openapiRoutes(t)

	var missing, extra []string
	for k := range oaSet {
		if _, ok := goSet[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k, perm := range goSet {
		if want, ok := oaSet[k]; !ok {
			extra = append(extra, k)
			continue
		} else if want != perm {
			t.Errorf("%s: router declares %q, openapi x-permission is %q", k, perm, want)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) != 0 || len(extra) != 0 {
		t.Errorf("route set mismatch:\nmissing from router:\n  %s\nextra in router:\n  %s",
			strings.Join(missing, "\n  "), strings.Join(extra, "\n  "))
	}
	if len(goSet) != len(oaSet) {
		t.Errorf("router has %d routes, openapi has %d", len(goSet), len(oaSet))
	}
	t.Logf("route set equal: %d routes", len(goSet))
}
