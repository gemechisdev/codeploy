package databases

import "testing"

func TestDetectAliases(t *testing.T) {
	yes := []string{"postgres:16", "docker.io/library/postgres:16", "ghcr.io/org/mirror-postgres:16", "mariadb:11", "redis:7", "valkey:8"}
	for _, img := range yes {
		if _, ok := Detect(img); !ok {
			t.Errorf("Detect(%q) want match", img)
		}
	}
	no := []string{"postgresql-exporter:latest", "nginx:alpine", "busybox"}
	for _, img := range no {
		if _, ok := Detect(img); ok {
			t.Errorf("Detect(%q) want no match", img)
		}
	}
}
