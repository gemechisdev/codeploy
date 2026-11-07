SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X github.com/codeplo/codeplo/internal/app.Version=$(VERSION) -X github.com/codeplo/codeplo/internal/app.Commit=$(COMMIT) -X github.com/codeplo/codeplo/internal/app.BuiltAt=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GEN_FILES := web/src/api/schema.d.ts internal/api/stubs_gen.go internal/api/oapigen/models.gen.go

.PHONY: gen gen-check lint test test-web migrate-check build run web dist dist-pack

# One definition for both targets: a change to api/openapi.yaml must regenerate all three files.
# The generators are pinned (web/package-lock.json openapi-typescript 7.13.0, go:generate @v2.8.0),
# so a clean checkout and CI produce byte-identical output. No recursive $(MAKE): its value contains
# spaces on the Windows GnuWin32 host, which /bin/sh cannot parse.
# oapi-codegen generates models only (api/oapi-codegen.yaml); handlers are hand-written, see docs/DECISIONS.md.
GEN_CMD := (cd web && npm run gen:api) && python3 scripts/gen_stubs.py && go generate ./internal/api/oapigen

gen:
	$(GEN_CMD)

# gen-check: explicit md5 idempotence + drift check for the three generated files.
# Generate twice and compare hashes: fail if generation is non-deterministic or committed files are stale.
gen-check:
	@set -eu; \
	before=$$(md5sum $(GEN_FILES)); \
	{ $(GEN_CMD); } >/dev/null; \
	first=$$(md5sum $(GEN_FILES)); \
	{ $(GEN_CMD); } >/dev/null; \
	second=$$(md5sum $(GEN_FILES)); \
	if [ "$$before" != "$$first" ]; then \
		echo "gen drift: committed files differ from 'make gen'; run make gen and commit" >&2; \
		printf 'committed:\n%s\ngenerated:\n%s\n' "$$before" "$$first" >&2; exit 1; \
	fi; \
	if [ "$$first" != "$$second" ]; then \
		echo "gen not idempotent: two runs produced different output" >&2; \
		printf 'run 1:\n%s\nrun 2:\n%s\n' "$$first" "$$second" >&2; exit 1; \
	fi; \
	echo "gen: idempotent and no drift ($(GEN_FILES))"

lint:
	go vet ./...
	@command -v golangci-lint >/dev/null && golangci-lint run || echo "golangci-lint not installed; skipped"

TEST_WEB_CMD := cd web && npm test -- --run

test-web:
	$(TEST_WEB_CMD)

# The frontend suite is part of `make test`, not a separate target nobody runs (T69). No recursive $(MAKE):
# its value has spaces on the Windows GnuWin32 host and /bin/sh cannot parse it.
test:
	go test ./...
	$(TEST_WEB_CMD)

# migrate-check: full migration chain reaches v8. -v + the RUN guard makes a renamed/missing
# test fail the target instead of silently passing with "no tests to run" (T1/T2).
migrate-check:
	@set -euo pipefail; \
	log=$$(mktemp); \
	trap 'rm -f "$$log"' EXIT; \
	go test -count=1 -v -run '^TestMigrateAllToV8$$' ./internal/store/ 2>&1 | tee "$$log"; \
	grep -qE '^=== RUN[[:space:]]+TestMigrateAllToV8$$' "$$log" || { echo "migrate-check: TestMigrateAllToV8 did not run" >&2; exit 1; }

web:
	cd web && npm ci && VITE_API_MODE=live npm run build

# build embeds web/dist through internal/webui (go:embed all:dist)
build: web
	rm -rf internal/webui/dist && mkdir -p internal/webui/dist && cp -r web/dist/. internal/webui/dist/ && touch internal/webui/dist/.gitkeep
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/codeplod ./cmd/codeplod
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/codeplo ./cmd/codeplo

run:
	CODEPLO_DATA_DIR=$${CODEPLO_DATA_DIR:-./.data} go run ./cmd/codeplod

# dist: the exact artefact set deploy/install.sh downloads, plus the checksums.txt it verifies.
# sha256sum -t forces GNU two-space text output (otherwise MSYS prints binary-mode " *"); a single
# arch still matches exactly 7 lines in the installer's grep -cE guard (both arch binaries present,
# the five static assets once). dist-pack is split out so CI can reuse the packaging after one web build.
DIST_ARCHS := amd64 arm64
# release.yml runs `goreleaser build --clean` first, which writes binaries named
# <name>_linux_<arch> straight into dist/. With DIST_FROM_GORELEASER=1 dist-pack reuses them
# instead of deleting them (the old `rm -rf dist` discarded the GoReleaser output) or rebuilding.
DIST_FROM_GORELEASER ?= 0
dist: web dist-pack

dist-pack:
	mkdir -p dist
	@if [ "$(DIST_FROM_GORELEASER)" != "1" ]; then rm -f dist/codeplod_linux_* dist/codeplo_linux_*; fi
	@for a in $(DIST_ARCHS); do \
		for b in codeplod codeplo; do \
			out="dist/$${b}_linux_$$a"; \
			if [ -f "$$out" ]; then continue; fi; \
			if [ "$(DIST_FROM_GORELEASER)" = "1" ]; then \
				echo "dist-pack: GoReleaser artefact $$out is missing" >&2; exit 1; \
			fi; \
			CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags '$(LDFLAGS)' -o "$$out" "./cmd/$$b" || exit 1; \
		done; \
	done
	cp deploy/codeplod.service deploy/caddy/codeplo-caddy.service deploy/codeplo-pty.socket 'deploy/codeplo-pty@.service' dist/
	cp deploy/caddy/bootstrap.json dist/caddy-bootstrap.json
	# install.sh is the release's own entry point: the one-line `curl .../releases/latest/download/install.sh`
	# command fetches the script from the SAME release as every artefact it then downloads. It is not in
	# checksums.txt (it is the verifier, not a payload), which keeps the installer's 7-entry assertion intact.
	cp deploy/install.sh dist/install.sh
	cd dist && sha256sum -t codeplod_linux_* codeplo_linux_* codeplod.service codeplo-caddy.service caddy-bootstrap.json codeplo-pty.socket 'codeplo-pty@.service' > checksums.txt
	@echo "dist/ contains $$(ls dist | wc -l) files; upload all of them to the release for the installer to verify"
