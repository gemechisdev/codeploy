package builders

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// CLI implements Builder with the git and docker command-line tools.
type CLI struct {
	Docker string // default "docker"
	Git    string // default "git"
	Socket string // docker socket, exported to the CLI as DOCKER_HOST
	TmpDir string // parent for credential temp dirs (0700); default os.TempDir()
}

var _ Builder = (*CLI)(nil)

func (c *CLI) docker() string {
	if c.Docker != "" {
		return c.Docker
	}
	return "docker"
}
func (c *CLI) git() string {
	if c.Git != "" {
		return c.Git
	}
	return "git"
}

func (c *CLI) tmp(prefix string) (string, error) {
	if c.TmpDir != "" {
		if err := os.MkdirAll(c.TmpDir, 0o700); err != nil {
			return "", err
		}
	}
	return os.MkdirTemp(c.TmpDir, prefix) // MkdirTemp creates the directory 0700
}

// hasBuildx reports whether `docker buildx version` works. Cached: install state does not change mid-process.
var (
	buildxOnce sync.Once
	buildxOK   bool
)

func dockerHasBuildx(dockerBin string) bool {
	buildxOnce.Do(func() {
		cmd := exec.Command(dockerBin, "buildx", "version")
		buildxOK = cmd.Run() == nil
	})
	return buildxOK
}

// baseEnv is deliberately small: nothing from the daemon's environment (master key path, tokens) reaches a build.
// DOCKER_BUILDKIT is only enabled when buildx is installed; otherwise Docker errors with "buildx component is missing".
func (c *CLI) baseEnv(home string) []string {
	buildkit := "0"
	if dockerHasBuildx(c.docker()) {
		buildkit = "1"
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LANG=C.UTF-8", "DOCKER_BUILDKIT=" + buildkit, "GIT_TERMINAL_PROMPT=0"}
	if c.Socket != "" {
		env = append(env, "DOCKER_HOST=unix://"+c.Socket)
	}
	return env
}

// writeDockerConfig writes an ephemeral DOCKER_CONFIG (dir 0700, file 0600). The caller removes the directory.
func (c *CLI) writeDockerConfig(a *RegistryAuth) (dir string, err error) {
	dir, err = c.tmp("dockercfg-")
	if err != nil {
		return "", err
	}
	cfg := map[string]any{"auths": map[string]any{}}
	if a != nil && a.Server != "" {
		tok := base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Password))
		cfg["auths"] = map[string]any{a.Server: map[string]string{"auth": tok}}
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// run executes one command, streaming its output line by line. It never uses a shell.
func run(ctx context.Context, dir string, env []string, log LogFunc, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var tail []string // last lines, for error classification
	pump := func(r io.Reader, stream string) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			l := sc.Text()
			mu.Lock()
			tail = append(tail, l)
			if len(tail) > 40 {
				tail = tail[1:]
			}
			mu.Unlock()
			if log != nil {
				log(stream, l)
			}
		}
	}
	wg.Add(2)
	go pump(stdout, "stdout")
	go pump(stderr, "stderr")
	wg.Wait()
	werr := cmd.Wait()
	mu.Lock()
	out := strings.Join(tail, "\n")
	mu.Unlock()
	if werr != nil && ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, werr
}

// classify maps tool output to the typed failures the engine reports with actionable text.
func classify(out string, err error) error {
	if err == nil {
		return nil
	}
	l := strings.ToLower(out + " " + err.Error())
	switch {
	case strings.Contains(l, "no space left on device"):
		return fmt.Errorf("%w: free disk space and retry (docker system prune, or the cleanup button)", ErrNoSpace)
	case strings.Contains(l, "signal: killed") || strings.Contains(l, "exit status 137") || strings.Contains(l, "oom") || strings.Contains(l, "out of memory"):
		return fmt.Errorf("%w: use a prebuilt image or a larger host", ErrOutOfMemory)
	}
	return err
}

func (c *CLI) Clone(ctx context.Context, src GitSource, dest string, log LogFunc) (CloneResult, error) {
	if err := ValidateGitURL(src.URL); err != nil {
		return CloneResult{}, err
	}
	if !branchRe.MatchString(src.Branch) {
		return CloneResult{}, fmt.Errorf("invalid branch name")
	}
	if len(src.DeployKey) > 0 && src.Token != "" {
		return CloneResult{}, fmt.Errorf("a source cannot use both a deploy key and a token")
	}
	cred, err := c.tmp("gitcred-")
	if err != nil {
		return CloneResult{}, err
	}
	defer os.RemoveAll(cred)
	env := c.baseEnv(cred)
	env = append(env, "GIT_ALLOW_PROTOCOL=https:ssh", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if len(src.DeployKey) > 0 {
		kf := filepath.Join(cred, "key")
		if err := os.WriteFile(kf, append(append([]byte{}, src.DeployKey...), '\n'), 0o600); err != nil {
			return CloneResult{}, err
		}
		env = append(env, "GIT_SSH_COMMAND=ssh -i "+kf+" -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="+filepath.Join(cred, "known_hosts"))
	}
	if src.Token != "" {
		// credential helper via GIT_ASKPASS: the token lives in a 0600 file read by a tiny script, never in a URL or argv
		tf := filepath.Join(cred, "token")
		if err := os.WriteFile(tf, []byte(src.Token), 0o600); err != nil {
			return CloneResult{}, err
		}
		ap := filepath.Join(cred, "askpass.sh")
		script := "#!/bin/sh\ncase \"$1\" in\n*sername*) echo x-access-token ;;\n*) cat '" + tf + "' ;;\nesac\n"
		if err := os.WriteFile(ap, []byte(script), 0o700); err != nil {
			return CloneResult{}, err
		}
		env = append(env, "GIT_ASKPASS="+ap)
	}
	// submodules are disabled; a shallow single-branch clone keeps disk and time bounded
	out, err := run(ctx, "", env, log, c.git(), "clone", "--depth", "1", "--single-branch", "--no-recurse-submodules", "--branch", src.Branch, "--", src.URL, dest)
	if err != nil {
		return CloneResult{}, classify(out, fmt.Errorf("git clone failed: %w", err))
	}
	// --depth bounds history, not size: a repository of large binaries clones in seconds and then fills the disk at
	// the build. The check is after the clone and before the build because the clone is the thing being bounded.
	if src.MaxBytes > 0 {
		used, err := dirBytes(dest)
		if err != nil {
			return CloneResult{}, err
		}
		if used > src.MaxBytes {
			return CloneResult{}, fmt.Errorf("%w: %s MB checked out, limit %s MB (git.max_clone_mb)",
				ErrSourceTooLarge, megabytes(used), megabytes(src.MaxBytes))
		}
	}
	sha, err := run(ctx, dest, env, nil, c.git(), "rev-parse", "HEAD")
	if err != nil {
		return CloneResult{}, err
	}
	msg, _ := run(ctx, dest, env, nil, c.git(), "log", "-1", "--pretty=%s")
	return CloneResult{Commit: strings.TrimSpace(lastLine(sha)), Message: strings.TrimSpace(lastLine(msg))}, nil
}

// dirBytes sums the regular files under root. Symlinks are not followed and directories are not counted: a
// repository must not be able to inflate the number, or the build context, by pointing outside the clone.
func dirBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func megabytes(n int64) string {
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// buildArgs assembles the `docker build` argument vector. Secrets are mounts (--secret), never --build-arg.
// With the legacy builder (no buildx), --progress=plain and --secret are omitted.
func buildArgs(req BuildRequest, secretFiles map[string]string, useBuildKit bool) []string {
	args := []string{"build", "--pull=false", "-t", req.Tag, "-f", req.Dockerfile}
	if useBuildKit {
		args = append(args, "--progress=plain")
	}
	if req.MemoryBytes > 0 {
		m := strconv.FormatInt(req.MemoryBytes, 10)
		args = append(args, "--memory", m, "--memory-swap", m)
	}
	akeys := make([]string, 0, len(req.Args))
	for k := range req.Args {
		akeys = append(akeys, k)
	}
	sort.Strings(akeys)
	for _, k := range akeys {
		args = append(args, "--build-arg", k+"="+req.Args[k])
	}
	if useBuildKit {
		ids := make([]string, 0, len(secretFiles))
		for id := range secretFiles {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			args = append(args, "--secret", "id="+id+",src="+secretFiles[id])
		}
	}
	return append(args, "--", req.ContextDir)
}

func (c *CLI) Build(ctx context.Context, req BuildRequest, log LogFunc) error {
	if !refRe.MatchString(req.Tag) {
		return fmt.Errorf("invalid image tag %q", req.Tag)
	}
	cfg, err := c.writeDockerConfig(req.Auth)
	if err != nil {
		return err
	}
	defer os.RemoveAll(cfg)
	sdir, err := c.tmp("buildsecrets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(sdir)
	files := map[string]string{}
	for id, v := range req.Secrets {
		if !secretIDRe.MatchString(id) {
			return fmt.Errorf("build secret id %q is not valid", id)
		}
		p := filepath.Join(sdir, id)
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			return err
		}
		files[id] = p
	}
	env := append(c.baseEnv(cfg), "DOCKER_CONFIG="+cfg)
	out, err := run(ctx, req.ContextDir, env, log, c.docker(), buildArgs(req, files, dockerHasBuildx(c.docker()))...)
	return classify(out, err)
}

func (c *CLI) Pull(ctx context.Context, req PullRequest, log LogFunc) error {
	if !refRe.MatchString(req.Ref) {
		return fmt.Errorf("invalid image reference %q", req.Ref)
	}
	cfg, err := c.writeDockerConfig(req.Auth)
	if err != nil {
		return err
	}
	defer os.RemoveAll(cfg)
	env := append(c.baseEnv(cfg), "DOCKER_CONFIG="+cfg)
	out, err := run(ctx, "", env, log, c.docker(), "pull", "--", req.Ref)
	return classify(out, err)
}

// ComposeUp runs `docker compose up -d --build` for a project directory.
// project is used as -p so containers are namespaced and easy to tear down.
func (c *CLI) ComposeUp(ctx context.Context, project, dir, composeFile string, log LogFunc) error {
	if composeFile == "" {
		composeFile = "compose.yaml"
		for _, n := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
			if st, err := os.Stat(filepath.Join(dir, n)); err == nil && !st.IsDir() {
				composeFile = n
				break
			}
		}
	}
	home, err := c.tmp("compose-home-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	env := c.baseEnv(home)
	out, err := run(ctx, dir, env, log, c.docker(), "compose", "-p", project, "-f", composeFile, "up", "-d", "--build", "--remove-orphans")
	return classify(out, err)
}
