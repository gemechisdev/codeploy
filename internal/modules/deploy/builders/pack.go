package builders

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DetectedPack is the build strategy chosen for a source tree without an operator-supplied Dockerfile.
type DetectedPack struct {
	Name       string // dockerfile | node | python | go | rust | php | ruby | static | unknown
	Dockerfile string // relative path under ContextDir that Build should use
	Port       int    // suggested listen port (0 = leave config unchanged)
	Note       string // one line for the deployment log
}

// EnsureDockerfile picks a Dockerfile for ContextDir. If a real Dockerfile (or Dockerfile.codegen already
// written by a previous deploy) exists, it is used. Otherwise a pack is detected from lockfiles/manifests
// and a Dockerfile.codegen is written — Coolify/Nixpacks-style auto build without requiring the operator
// to author a Dockerfile for common stacks.
func EnsureDockerfile(contextDir, preferred string, log LogFunc) (DetectedPack, error) {
	if preferred == "" {
		preferred = "Dockerfile"
	}
	// Absolute preferred is already confined by the caller; treat as basename for lookup.
	try := []string{preferred, "Dockerfile", "dockerfile", "Dockerfile.codegen"}
	for _, name := range try {
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(contextDir, name)
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			rel := name
			if filepath.IsAbs(name) {
				rel, _ = filepath.Rel(contextDir, name)
			}
			return DetectedPack{Name: "dockerfile", Dockerfile: rel, Note: "using " + rel}, nil
		}
	}

	pack, content, port := detectPack(contextDir)
	if pack == "unknown" || content == "" {
		return DetectedPack{}, fmt.Errorf("no Dockerfile found and no supported stack detected (looked for package.json, go.mod, Cargo.toml, pyproject.toml/requirements.txt, composer.json, Gemfile, or index.html). Add a Dockerfile or use Pull image")
	}
	out := filepath.Join(contextDir, "Dockerfile.codegen")
	if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
		return DetectedPack{}, err
	}
	if log != nil {
		log("system", "auto-detected "+pack+" stack; wrote Dockerfile.codegen")
	}
	return DetectedPack{Name: pack, Dockerfile: "Dockerfile.codegen", Port: port, Note: "auto-detected " + pack}, nil
}

func exists(dir, name string) bool {
	st, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !st.IsDir()
}

func readFile(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}

func detectPack(dir string) (name, dockerfile string, port int) {
	// Node / JS frameworks (Vercel-style)
	if exists(dir, "package.json") {
		pkg := readFile(dir, "package.json")
		lock := "npm ci || npm install"
		if exists(dir, "pnpm-lock.yaml") {
			lock = "corepack enable && pnpm install --frozen-lockfile"
		} else if exists(dir, "yarn.lock") {
			lock = "yarn install --frozen-lockfile"
		} else if exists(dir, "bun.lockb") || exists(dir, "bun.lock") {
			lock = "npm install -g bun && bun install --frozen-lockfile"
		}
		// Next.js
		if strings.Contains(pkg, `"next"`) {
			return "next", fmt.Sprintf(`# syntax=docker/dockerfile:1
FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* bun.lockb* bun.lock* ./
RUN %s
FROM node:22-alpine AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build
FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000
COPY --from=build /app ./
EXPOSE 3000
CMD ["npm","run","start"]
`, lock), 3000
		}
		// Vite / CRA static export → nginx
		if strings.Contains(pkg, `"vite"`) || strings.Contains(pkg, `"react-scripts"`) || strings.Contains(pkg, `"vue"`) || strings.Contains(pkg, `"nuxt"`) {
			buildCmd := "npm run build"
			outDir := "dist"
			if strings.Contains(pkg, `"react-scripts"`) {
				outDir = "build"
			}
			if strings.Contains(pkg, `"nuxt"`) {
				outDir = ".output/public"
			}
			return "node-static", fmt.Sprintf(`# syntax=docker/dockerfile:1
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* ./
RUN %s
COPY . .
RUN %s
FROM nginx:1.27-alpine
COPY --from=build /app/%s /usr/share/nginx/html
EXPOSE 80
`, lock, buildCmd, outDir), 80
		}
		// Generic Node API
		return "node", fmt.Sprintf(`# syntax=docker/dockerfile:1
FROM node:22-alpine
WORKDIR /app
COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* ./
RUN %s
COPY . .
ENV PORT=3000
EXPOSE 3000
CMD ["npm","start"]
`, lock), 3000
	}

	// Python
	if exists(dir, "requirements.txt") || exists(dir, "pyproject.toml") || exists(dir, "Pipfile") {
		install := "pip install --no-cache-dir -r requirements.txt"
		if exists(dir, "pyproject.toml") && !exists(dir, "requirements.txt") {
			install = "pip install --no-cache-dir ."
		}
		cmd := `["python","-m","http.server","8000"]`
		if exists(dir, "main.py") {
			// Prefer common ASGI/WSGI entrypoints when present in tree markers
			cmd = `["python","main.py"]`
		}
		if strings.Contains(readFile(dir, "requirements.txt")+readFile(dir, "pyproject.toml"), "uvicorn") {
			cmd = `["uvicorn","main:app","--host","0.0.0.0","--port","8000"]`
		}
		if strings.Contains(readFile(dir, "requirements.txt")+readFile(dir, "pyproject.toml"), "gunicorn") {
			cmd = `["gunicorn","-b","0.0.0.0:8000","app:app"]`
		}
		if strings.Contains(readFile(dir, "requirements.txt"), "flask") || strings.Contains(readFile(dir, "requirements.txt"), "django") {
			// keep python main or gunicorn heuristics above
		}
		return "python", fmt.Sprintf(`# syntax=docker/dockerfile:1
FROM python:3.12-slim
WORKDIR /app
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PORT=8000
COPY requirements.txt* pyproject.toml* Pipfile* ./
RUN pip install --no-cache-dir -U pip && (%s || true)
COPY . .
EXPOSE 8000
CMD %s
`, install, cmd), 8000
	}

	// Go
	if exists(dir, "go.mod") {
		return "go", `# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/app .
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/app /app/app
ENV PORT=8080
EXPOSE 8080
CMD ["/app/app"]
`, 8080
	}

	// Rust
	if exists(dir, "Cargo.toml") {
		return "rust", `# syntax=docker/dockerfile:1
FROM rust:1.83-alpine AS build
RUN apk add --no-cache musl-dev
WORKDIR /src
COPY Cargo.toml Cargo.lock* ./
COPY src ./src
RUN cargo build --release
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /src/target/release/* /app/app
ENV PORT=8080
EXPOSE 8080
CMD ["/app/app"]
`, 8080
	}

	// PHP
	if exists(dir, "composer.json") {
		return "php", `# syntax=docker/dockerfile:1
FROM php:8.3-cli
WORKDIR /app
COPY . .
RUN if [ -f composer.json ]; then curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer && composer install --no-dev --optimize-autoloader || true; fi
ENV PORT=8000
EXPOSE 8000
CMD ["php","-S","0.0.0.0:8000","-t","public"]
`, 8000
	}

	// Ruby
	if exists(dir, "Gemfile") {
		return "ruby", `# syntax=docker/dockerfile:1
FROM ruby:3.3-alpine
WORKDIR /app
COPY Gemfile Gemfile.lock* ./
RUN bundle install --without development test || bundle install
COPY . .
ENV PORT=3000
EXPOSE 3000
CMD ["bundle","exec","rackup","-o","0.0.0.0","-p","3000"]
`, 3000
	}

	// Plain static (Vercel-like static hosting)
	if exists(dir, "index.html") {
		return "static", `# syntax=docker/dockerfile:1
FROM nginx:1.27-alpine
COPY . /usr/share/nginx/html
EXPOSE 80
`, 80
	}

	return "unknown", "", 0
}
