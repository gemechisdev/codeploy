package builders

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildPlan is the resolved install/build/start pipeline for one context directory.
// Empty override fields mean "use auto-detected defaults".
type BuildPlan struct {
	Pack           string // node, next, python, go, rust, php, ruby, static, dockerfile, unknown
	InstallCommand string
	BuildCommand   string // may be empty
	StartCommand   string
	TestCommand    string // optional; not run in image by default
	Runtime        string // e.g. node:22-alpine, python:3.12-slim
	Port           int
	Dockerfile     string // relative path under context to use for docker build
	Note           string
}

// BuildOverrides are operator-supplied commands (Render/Koyeb/Vercel-style). Any non-empty
// field replaces the auto-detected value for that step.
type BuildOverrides struct {
	InstallCommand string
	BuildCommand   string
	StartCommand   string
	TestCommand    string
	Runtime        string // base image tag or runtime id (node:22, python:3.12, …)
	// Dockerfile forces a path; when set and the file exists, pack is "dockerfile" and commands are ignored for the image build.
	Dockerfile string
}

// ResolveBuild detects the stack under contextDir and applies overrides.
// Prefer an existing Dockerfile unless the operator cleared it in favor of command-based builds.
func ResolveBuild(contextDir string, o BuildOverrides) (BuildPlan, error) {
	// Explicit Dockerfile path that exists wins (classic path).
	dfName := o.Dockerfile
	if dfName == "" {
		dfName = "Dockerfile"
	}
	for _, name := range []string{dfName, "Dockerfile", "dockerfile"} {
		p := filepath.Join(contextDir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			// Operator may still want command overrides later; for image build we use the file as-is.
			return BuildPlan{
				Pack:       "dockerfile",
				Dockerfile: name,
				Port:       0,
				Note:       "using " + name,
			}, nil
		}
	}

	plan := detectPlan(contextDir)
	if plan.Pack == "unknown" {
		return BuildPlan{}, fmt.Errorf("no Dockerfile found and no supported stack detected (package.json, go.mod, Cargo.toml, pyproject.toml/requirements.txt, composer.json, Gemfile, or index.html). Add a Dockerfile or set install/build/start commands")
	}

	// Apply overrides (empty means keep detected).
	if s := strings.TrimSpace(o.InstallCommand); s != "" {
		plan.InstallCommand = s
	}
	if s := strings.TrimSpace(o.BuildCommand); s != "" {
		plan.BuildCommand = s
	}
	if s := strings.TrimSpace(o.StartCommand); s != "" {
		plan.StartCommand = s
	}
	if s := strings.TrimSpace(o.TestCommand); s != "" {
		plan.TestCommand = s
	}
	if s := strings.TrimSpace(o.Runtime); s != "" {
		plan.Runtime = normalizeRuntime(s, plan.Pack)
	}

	content, err := renderDockerfile(plan)
	if err != nil {
		return BuildPlan{}, err
	}
	out := filepath.Join(contextDir, "Dockerfile.codegen")
	if err := os.WriteFile(out, []byte(content), 0o644); err != nil {
		return BuildPlan{}, err
	}
	plan.Dockerfile = "Dockerfile.codegen"
	plan.Note = fmt.Sprintf("auto %s · install=%q build=%q start=%q", plan.Pack, plan.InstallCommand, plan.BuildCommand, plan.StartCommand)
	return plan, nil
}

// EnsureDockerfile is the engine entry: resolve + write Dockerfile.codegen when needed.
func EnsureDockerfile(contextDir, preferred string, log LogFunc) (DetectedPack, error) {
	o := BuildOverrides{Dockerfile: preferred}
	plan, err := ResolveBuild(contextDir, o)
	if err != nil {
		return DetectedPack{}, err
	}
	if log != nil {
		log("system", plan.Note)
	}
	return DetectedPack{Name: plan.Pack, Dockerfile: plan.Dockerfile, Port: plan.Port, Note: plan.Note}, nil
}

// EnsureDockerfileWithOverrides is used when the resource config carries build settings.
func EnsureDockerfileWithOverrides(contextDir string, o BuildOverrides, log LogFunc) (DetectedPack, error) {
	plan, err := ResolveBuild(contextDir, o)
	if err != nil {
		return DetectedPack{}, err
	}
	if log != nil {
		log("system", plan.Note)
	}
	return DetectedPack{Name: plan.Pack, Dockerfile: plan.Dockerfile, Port: plan.Port, Note: plan.Note}, nil
}

// DetectedPack is the slim result the deploy engine needs.
type DetectedPack struct {
	Name       string
	Dockerfile string
	Port       int
	Note       string
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

func detectPlan(dir string) BuildPlan {
	// Node ecosystem
	if exists(dir, "package.json") {
		pkg := readFile(dir, "package.json")
		var meta struct {
			Scripts map[string]string `json:"scripts"`
		}
		_ = json.Unmarshal([]byte(pkg), &meta)
		install := "npm ci || npm install"
		if exists(dir, "pnpm-lock.yaml") {
			install = "corepack enable && pnpm install --frozen-lockfile"
		} else if exists(dir, "yarn.lock") {
			install = "yarn install --frozen-lockfile"
		} else if exists(dir, "bun.lockb") || exists(dir, "bun.lock") {
			install = "npm i -g bun && bun install --frozen-lockfile"
		}

		has := func(s string) bool { return meta.Scripts != nil && meta.Scripts[s] != "" }
		build, start := "", "npm start"
		if has("build") {
			build = "npm run build"
		}
		if has("start") {
			start = "npm start"
		} else if has("preview") {
			start = "npm run preview"
		}

		// Next.js
		if strings.Contains(pkg, `"next"`) {
			if !has("build") {
				build = "npx next build"
			}
			if !has("start") {
				start = "npx next start -H 0.0.0.0 -p 3000"
			}
			return BuildPlan{Pack: "next", InstallCommand: install, BuildCommand: build, StartCommand: start,
				Runtime: "node:22-alpine", Port: 3000}
		}
		// Static frontends → build then nginx
		if strings.Contains(pkg, `"vite"`) || strings.Contains(pkg, `"react-scripts"`) ||
			strings.Contains(pkg, `"vue"`) || strings.Contains(pkg, `"nuxt"`) || strings.Contains(pkg, `"astro"`) {
			outDir := "dist"
			if strings.Contains(pkg, `"react-scripts"`) {
				outDir = "build"
			}
			if strings.Contains(pkg, `"nuxt"`) {
				outDir = ".output/public"
			}
			if !has("build") {
				build = "npm run build"
			}
			return BuildPlan{Pack: "node-static", InstallCommand: install, BuildCommand: build,
				StartCommand: "nginx -g 'daemon off;'", Runtime: "node:22-alpine", Port: 8080,
				Note: outDir}
		}
		return BuildPlan{Pack: "node", InstallCommand: install, BuildCommand: build, StartCommand: start,
			Runtime: "node:22-alpine", Port: 3000}
	}

	// Python
	if exists(dir, "requirements.txt") || exists(dir, "pyproject.toml") || exists(dir, "Pipfile") {
		install := "pip install --no-cache-dir -U pip"
		if exists(dir, "requirements.txt") {
			install += " && pip install --no-cache-dir -r requirements.txt"
		} else if exists(dir, "pyproject.toml") {
			install += " && pip install --no-cache-dir ."
		}
		start := "python -m http.server 8000"
		req := readFile(dir, "requirements.txt") + readFile(dir, "pyproject.toml")
		if strings.Contains(req, "uvicorn") {
			start = "uvicorn main:app --host 0.0.0.0 --port 8000"
		} else if strings.Contains(req, "gunicorn") {
			start = "gunicorn -b 0.0.0.0:8000 app:app"
		} else if exists(dir, "main.py") {
			start = "python main.py"
		} else if exists(dir, "app.py") {
			start = "python app.py"
		} else if exists(dir, "manage.py") {
			start = "python manage.py runserver 0.0.0.0:8000"
		}
		return BuildPlan{Pack: "python", InstallCommand: install, StartCommand: start,
			Runtime: "python:3.12-slim", Port: 8000}
	}

	// Go
	if exists(dir, "go.mod") {
		return BuildPlan{Pack: "go", InstallCommand: "go mod download",
			BuildCommand: "CGO_ENABLED=0 go build -o /out/app .", StartCommand: "/out/app",
			Runtime: "golang:1.23-alpine", Port: 8080}
	}

	// Rust
	if exists(dir, "Cargo.toml") {
		return BuildPlan{Pack: "rust", InstallCommand: "true",
			BuildCommand: "cargo build --release", StartCommand: "./target/release/app",
			Runtime: "rust:1.83-alpine", Port: 8080}
	}

	// PHP
	if exists(dir, "composer.json") {
		return BuildPlan{Pack: "php",
			InstallCommand: "curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer && composer install --no-dev --optimize-autoloader",
			StartCommand:   "php -S 0.0.0.0:8000 -t public",
			Runtime:        "php:8.3-cli", Port: 8000}
	}

	// Ruby
	if exists(dir, "Gemfile") {
		return BuildPlan{Pack: "ruby", InstallCommand: "bundle install --without development test",
			StartCommand: "bundle exec rackup -o 0.0.0.0 -p 3000",
			Runtime:      "ruby:3.3-alpine", Port: 3000}
	}

	// Plain static
	if exists(dir, "index.html") {
		return BuildPlan{Pack: "static", InstallCommand: "true", StartCommand: "nginx -g 'daemon off;'",
			Runtime: "nginxinc/nginx-unprivileged:1.27-alpine", Port: 8080}
	}

	return BuildPlan{Pack: "unknown"}
}

func normalizeRuntime(s, pack string) string {
	s = strings.TrimSpace(s)
	// Allow short forms: node:22, python:3.12
	if !strings.Contains(s, "/") && strings.Count(s, ":") == 1 {
		return s
	}
	return s
}

func renderDockerfile(p BuildPlan) (string, error) {
	switch p.Pack {
	case "dockerfile":
		return "", fmt.Errorf("internal: dockerfile pack should not render")
	case "static":
		// nginx:alpine workers setuid to the nginx user — fails under no-new-privileges.
		// nginx-unprivileged runs entirely as uid 101 and listens on 8080.
		return `FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY --chown=101:101 . /usr/share/nginx/html
EXPOSE 8080
`, nil
	case "node-static":
		outDir := p.Note
		if outDir == "" {
			outDir = "dist"
		}
		p.Note = ""
		return fmt.Sprintf(`FROM %s AS build
WORKDIR /app
COPY package.json package-lock.json* pnpm-lock.yaml* yarn.lock* bun.lockb* bun.lock* ./
RUN %s
COPY . .
RUN %s
FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY --from=build --chown=101:101 /app/%s /usr/share/nginx/html
EXPOSE 8080
`, p.Runtime, p.InstallCommand, nullCmd(p.BuildCommand, "npm run build"), outDir), nil
	case "go":
		return fmt.Sprintf(`FROM %s AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN %s
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/app /app/app
ENV PORT=%d
EXPOSE %d
CMD ["/app/app"]
`, p.Runtime, nullCmd(p.BuildCommand, "CGO_ENABLED=0 go build -o /out/app ."), portOr(p.Port, 8080), portOr(p.Port, 8080)), nil
	case "rust":
		return fmt.Sprintf(`FROM %s AS build
RUN apk add --no-cache musl-dev
WORKDIR /src
COPY Cargo.toml Cargo.lock* ./
COPY src ./src
RUN %s
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /src/target/release /app/bin
ENV PORT=%d
EXPOSE %d
CMD ["sh","-c","exec /app/bin/$(ls /app/bin | head -1)"]
`, p.Runtime, nullCmd(p.BuildCommand, "cargo build --release"), portOr(p.Port, 8080), portOr(p.Port, 8080)), nil
	default:
		// Generic single-stage: install → optional build → start
		port := portOr(p.Port, 3000)
		buildLine := ""
		if strings.TrimSpace(p.BuildCommand) != "" {
			buildLine = "RUN " + p.BuildCommand + "\n"
		}
		return fmt.Sprintf(`FROM %s
WORKDIR /app
ENV PORT=%d HOST=0.0.0.0
COPY . .
RUN %s
%sEXPOSE %d
CMD ["sh","-c",%q]
`, p.Runtime, port, nullCmd(p.InstallCommand, "true"), buildLine, port, p.StartCommand), nil
	}
}

func nullCmd(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func portOr(p, def int) int {
	if p > 0 {
		return p
	}
	return def
}
