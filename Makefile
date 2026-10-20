SHELL := /bin/bash
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X github.com/codeplo/codeplo/internal/app.Version=$(VERSION) -X github.com/codeplo/codeplo/internal/app.Commit=$(COMMIT) -X github.com/codeplo/codeplo/internal/app.BuiltAt=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: gen lint test build run web

# gen: TS client types from openapi.yaml, later-phase route stubs (Go) from the same file, sqlc.
# oapi-codegen generates models only (api/oapi-codegen.yaml); handlers are hand-written, see docs/DECISIONS.md.
gen:
	cd web && npm run gen:api
	python3 scripts/gen_stubs.py
	go generate ./internal/api/oapigen
	@command -v sqlc >/dev/null && sqlc generate || echo "sqlc not installed; skipped (no queries yet)"

lint:
	go vet ./...
	@command -v golangci-lint >/dev/null && golangci-lint run || echo "golangci-lint not installed; skipped"

test:
	go test ./...

web:
	cd web && npm ci && npm run build

# build embeds web/dist through internal/webui (go:embed all:dist)
build: web
	rm -rf internal/webui/dist && mkdir -p internal/webui/dist && cp -r web/dist/. internal/webui/dist/ && touch internal/webui/dist/.gitkeep
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/codeplod ./cmd/codeplod
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/codeplo ./cmd/codeplo

run:
	CODEPLO_DATA_DIR=$${CODEPLO_DATA_DIR:-./.data} go run ./cmd/codeplod
