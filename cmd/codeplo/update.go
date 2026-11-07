// `codeplo update` and `codeplo rollback`: break-glass release handling on the host, with the same trust model
// as the other CLI commands (run as root or the service user, no HTTP route, no new dependencies).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/store"
)

const (
	defaultReleaseRepo   = "gemechisdev/codeploy"
	defaultCosignIssuer  = "https://token.actions.githubusercontent.com"
	latestReleaseVersion = "latest"
)

var (
	updateStampRe    = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}\.[0-9]+$`)
	releaseVersionRe = regexp.MustCompile(`^(latest|v[0-9][A-Za-z0-9._+-]*)$`)
	releaseRepoRe    = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

type updateManifest struct {
	Version        string            `json:"version"`
	Stamp          string            `json:"stamp"`
	Paths          map[string]string `json:"paths"`
	Sizes          map[string]int64  `json:"sizes"`
	SHA256         map[string]string `json:"sha256"`
	DBSnapshotName string            `json:"db_snapshot_name"`
}

// releaseAssets is the exact set deploy/install.sh checksums (the arch-specific binaries plus the units and
// bootstrap config). checksums.txt itself is the manifest, not an asset, so it is absent here.
func releaseAssets() []string {
	arch := runtime.GOARCH
	return []string{
		"codeplod_linux_" + arch,
		"codeplo_linux_" + arch,
		"codeplod.service",
		"codeplo-caddy.service",
		"caddy-bootstrap.json",
		"codeplo-pty.socket",
		"codeplo-pty@.service",
	}
}

func updateCommand(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	from := fs.String("from", "", "local directory holding the release artefacts (preferred)")
	binDir := fs.String("bin-dir", "/usr/local/bin", "directory holding the installed codeplod and codeplo binaries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	cosignPath, cosignErr := exec.LookPath("cosign")
	identity := os.Getenv("CODEPLO_COSIGN_IDENTITY")
	if identity == "" {
		// The installer pins this same workflow identity. An operator with cosign installed should get
		// signature verification without having to discover and export the regexp first.
		identity = defaultCosignIdentity(releaseRepo())
	}
	issuer := os.Getenv("CODEPLO_COSIGN_ISSUER")
	if issuer == "" {
		issuer = defaultCosignIssuer
	}
	if os.Getenv("CODEPLO_REQUIRE_COSIGN") == "1" && (cosignErr != nil || identity == "") {
		return fmt.Errorf("CODEPLO_REQUIRE_COSIGN=1 but cosign or CODEPLO_COSIGN_IDENTITY is unavailable: refusing to update without signature verification")
	}
	wantCosign := cosignErr == nil

	srcDir, cleanup, err := resolveSource(*from, wantCosign)
	if err != nil {
		return err
	}
	defer cleanup()

	// Every check below is read-only: nothing on the host is written until all of them pass.
	if err := verifyAssets(srcDir); err != nil {
		return err
	}
	if err := verifyCosign(srcDir, cosignPath, identity, issuer, wantCosign); err != nil {
		return err
	}

	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	backupDir := filepath.Join(cfg.DataDir, "update-backups", stamp)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(backupDir)
		}
	}()

	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close()
	snapName := "codeplo.db.snapshot"
	snapPath := filepath.Join(backupDir, snapName)
	// VACUUM INTO writes a consistent standalone copy through the open *sql.DB, so WAL and a running daemon need
	// no special handling. The filename is a literal, quoted here to keep it injection-safe.
	if _, err := db.Exec(fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(snapPath, "'", "''"))); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}

	paths := map[string]string{
		"codeplod": filepath.Join(*binDir, "codeplod"),
		"codeplo":  filepath.Join(*binDir, "codeplo"),
		"db":       cfg.DBPath(),
	}
	backupNames := map[string]string{
		"codeplod": "codeplod",
		"codeplo":  "codeplo",
		"db":       snapName,
	}
	for _, key := range []string{"codeplod", "codeplo"} {
		if err := writeFileAtomic(paths[key], filepath.Join(backupDir, backupNames[key]), 0o755); err != nil {
			return err
		}
	}
	sizes := map[string]int64{}
	sums := map[string]string{}
	for key, name := range backupNames {
		n, h, err := fileInfo(filepath.Join(backupDir, name))
		if err != nil {
			return err
		}
		sizes[key], sums[key] = n, h
	}

	version := os.Getenv("CODEPLO_VERSION")
	if version == "" {
		version = latestReleaseVersion
	}
	m := updateManifest{
		Version:        version,
		Stamp:          stamp,
		Paths:          paths,
		Sizes:          sizes,
		SHA256:         sums,
		DBSnapshotName: snapName,
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backupDir, "manifest.json"), out, 0o600); err != nil {
		return err
	}

	// Stage in the destination directory and rename, so a running executable is never overwritten in place.
	if err := writeFileAtomic(filepath.Join(srcDir, "codeplod_linux_"+runtime.GOARCH), paths["codeplod"], 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(srcDir, "codeplo_linux_"+runtime.GOARCH), paths["codeplo"], 0o755); err != nil {
		return err
	}

	done = true
	fmt.Printf("updated to %s; backup at %s\n", version, backupDir)
	fmt.Println("restart codeplod to run the new binaries; migrations are applied by the daemon on its next boot, not by this command")
	return nil
}

// releaseRepo is the official public repository unless a fork or mirror explicitly overrides it.
// It is validated only when an https release URL is actually constructed, so `codeplo update --from DIR`
// remains usable offline.
func releaseRepo() string {
	if repo := os.Getenv("CODEPLO_REPO"); repo != "" {
		return repo
	}
	return defaultReleaseRepo
}

// defaultCosignIdentity mirrors the regexp deploy/install.sh pins. QuoteMeta keeps a repo name
// containing a dot literal instead of turning it into a wildcard.
func defaultCosignIdentity(repo string) string {
	return `^https://github\.com/` + regexp.QuoteMeta(repo) +
		`/\.github/workflows/release\.yml@refs/tags/v[0-9].*$`
}

func releaseVersion() (string, error) {
	ver := os.Getenv("CODEPLO_VERSION")
	if ver == "" {
		return latestReleaseVersion, nil
	}
	if !releaseVersionRe.MatchString(ver) || strings.Contains(ver, "..") {
		return "", fmt.Errorf("invalid CODEPLO_VERSION %q (use latest or a v-prefixed tag)", ver)
	}
	return ver, nil
}

// releaseBaseURL is the shared release source for both the installer and `codeplo update`: the
// newest non-draft, non-prerelease GitHub release by default, a pinned tag via CODEPLO_VERSION,
// or an operator-owned https mirror via CODEPLO_BASE_URL.
func releaseBaseURL() (string, error) {
	if override := os.Getenv("CODEPLO_BASE_URL"); override != "" {
		return validateReleaseBaseURL(override)
	}
	repo := releaseRepo()
	if !releaseRepoRe.MatchString(repo) || strings.Contains(repo, "..") {
		return "", fmt.Errorf("invalid CODEPLO_REPO %q (expected owner/repo)", repo)
	}
	ver, err := releaseVersion()
	if err != nil {
		return "", err
	}
	if ver == latestReleaseVersion {
		return fmt.Sprintf("https://github.com/%s/releases/latest/download", repo), nil
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, url.PathEscape(ver)), nil
}

func validateReleaseBaseURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("release base URL must be a plain https URL (got %q)", base)
	}
	return strings.TrimRight(base, "/"), nil
}

func resolveSource(from string, wantCosign bool) (string, func(), error) {
	if from != "" {
		return from, func() {}, nil
	}
	base, err := releaseBaseURL()
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp("", "codeplo-update-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	client := newDownloadClient()
	files := append(releaseAssets(), "checksums.txt")
	if wantCosign {
		files = append(files, "checksums.txt.sig", "checksums.txt.pem")
	}
	for _, name := range files {
		if err := downloadFile(client, base, name, tmp); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return tmp, cleanup, nil
}

// stallTimeout aborts a transfer that has delivered no bytes for this long. It replaces a fixed total timeout, which
// fails a healthy but slow link (release binaries are several MB) while still catching a dead one.
var stallTimeout = 60 * time.Second

func newDownloadClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
	}}
}

// idleReader resets the stall timer whenever data arrives.
type idleReader struct {
	r io.Reader
	t *time.Timer
}

func (i idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.t.Reset(stallTimeout)
	}
	return n, err
}

func downloadFile(client *http.Client, base, name, dir string) error {
	u := strings.TrimRight(base, "/") + "/" + name
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "https" {
		return fmt.Errorf("refusing non-https download URL %q", u)
	}
	var last error
	for attempt := 1; attempt <= 3; attempt++ {
		fmt.Fprintf(os.Stderr, "downloading %s (attempt %d/3)\n", name, attempt)
		if last = downloadOnce(client, u, name, dir); last == nil {
			return nil
		}
		fmt.Fprintf(os.Stderr, "  failed: %v\n", last)
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
	return last
}

func downloadOnce(client *http.Client, u, name, dir string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stall := time.AfterFunc(stallTimeout, cancel)
	defer stall.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
	}
	stall.Reset(stallTimeout)
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, idleReader{r: resp.Body, t: stall}); err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	return nil
}

func parseChecksums(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 67 {
			return nil, fmt.Errorf("malformed checksum line %q", line)
		}
		sep := line[64:66]
		if sep != "  " && sep != " *" {
			return nil, fmt.Errorf("malformed checksum line %q", line)
		}
		hash := strings.ToLower(line[:64])
		if _, err := hex.DecodeString(hash); err != nil {
			return nil, fmt.Errorf("malformed checksum hash in %q", line)
		}
		name := line[66:]
		if !safeAssetName(name) {
			return nil, fmt.Errorf("checksums.txt has an unsafe filename %q", name)
		}
		if _, ok := m[name]; ok {
			return nil, fmt.Errorf("duplicate checksum entry for %q", name)
		}
		m[name] = hash
	}
	return m, nil
}

// safeAssetName refuses separators, absolute paths and dot segments, so a crafted checksum line can never name a
// file outside the release directory or masquerade as an expected asset.
func safeAssetName(name string) bool {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	return filepath.Base(name) == name
}

func verifyAssets(dir string) error {
	sums, err := parseChecksums(dir)
	if err != nil {
		return err
	}
	for _, name := range releaseAssets() {
		want, ok := sums[name]
		if !ok {
			return fmt.Errorf("checksums.txt is missing an entry for %s", name)
		}
		_, got, err := fileInfo(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("verify %s: %w", name, err)
		}
		if got != want {
			return fmt.Errorf("SHA-256 mismatch for %s", name)
		}
	}
	return nil
}

func verifyCosign(dir, cosignPath, identity, issuer string, wantCosign bool) error {
	if !wantCosign {
		fmt.Fprintln(os.Stderr, "WARNING: cosign signature not verified (cosign is not installed); only SHA-256 checks apply")
		return nil
	}
	for _, f := range []string{"checksums.txt.sig", "checksums.txt.pem"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return fmt.Errorf("cosign verification needs %s in the release directory", f)
		}
	}
	cmd := exec.Command(cosignPath, "verify-blob",
		"--certificate", "checksums.txt.pem",
		"--signature", "checksums.txt.sig",
		"--certificate-identity-regexp", identity,
		"--certificate-oidc-issuer", issuer,
		"checksums.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cosign verification failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("cosign signature verified")
	return nil
}

func fileInfo(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func writeFileAtomic(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".codeplo-update-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, dst)
}

func rollbackCommand(args []string) error {
	fs := flag.NewFlagSet("rollback", flag.ExitOnError)
	stampFlag := fs.String("stamp", "", "backup stamp to restore (default: most recent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	backupsDir := filepath.Join(cfg.DataDir, "update-backups")
	stamp, err := selectBackup(backupsDir, *stampFlag)
	if err != nil {
		return err
	}
	backupDir := filepath.Join(backupsDir, stamp)
	m, err := readManifest(backupDir)
	if err != nil {
		return err
	}
	// Verify the manifest hashes and the daemon state before a single file is written.
	if err := verifyBackup(backupDir, m); err != nil {
		return err
	}
	if err := refuseIfActive(); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(backupDir, "codeplod"), m.Paths["codeplod"], 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(backupDir, "codeplo"), m.Paths["codeplo"], 0o755); err != nil {
		return err
	}
	if err := restoreDatabase(filepath.Join(backupDir, m.DBSnapshotName), m.Paths["db"]); err != nil {
		return err
	}
	fmt.Printf("restored backup %s (version %s); backup retained at %s\n", stamp, m.Version, backupDir)
	fmt.Println("migrations are forward-only: this restored a database snapshot, it did not reverse any migration")
	return nil
}

func selectBackup(backupsDir, want string) (string, error) {
	if want != "" {
		if !updateStampRe.MatchString(want) {
			return "", fmt.Errorf("invalid backup stamp %q", want)
		}
		if _, err := os.Stat(filepath.Join(backupsDir, want, "manifest.json")); err != nil {
			return "", fmt.Errorf("no backup for stamp %q in %s", want, backupsDir)
		}
		return want, nil
	}
	ents, err := os.ReadDir(backupsDir)
	if err != nil {
		return "", fmt.Errorf("no update backups in %s: %w", backupsDir, err)
	}
	var stamps []string
	for _, e := range ents {
		if e.IsDir() && updateStampRe.MatchString(e.Name()) {
			if _, err := os.Stat(filepath.Join(backupsDir, e.Name(), "manifest.json")); err == nil {
				stamps = append(stamps, e.Name())
			}
		}
	}
	if len(stamps) == 0 {
		return "", fmt.Errorf("no update backups in %s", backupsDir)
	}
	sort.Strings(stamps)
	return stamps[len(stamps)-1], nil
}

func readManifest(backupDir string) (updateManifest, error) {
	var m updateManifest
	data, err := os.ReadFile(filepath.Join(backupDir, "manifest.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("parse manifest.json: %w", err)
	}
	return m, nil
}

func verifyBackup(backupDir string, m updateManifest) error {
	if !safeAssetName(m.DBSnapshotName) {
		return fmt.Errorf("manifest has an unsafe db_snapshot_name %q", m.DBSnapshotName)
	}
	for _, key := range []string{"codeplod", "codeplo", "db"} {
		if p := m.Paths[key]; p == "" || !filepath.IsAbs(p) {
			return fmt.Errorf("manifest has an invalid path for %s: %q", key, p)
		}
	}
	names := map[string]string{
		"codeplod": "codeplod",
		"codeplo":  "codeplo",
		"db":       m.DBSnapshotName,
	}
	for key, name := range names {
		wantSize, ok := m.Sizes[key]
		if !ok {
			return fmt.Errorf("manifest missing size for %s", key)
		}
		wantHash, ok := m.SHA256[key]
		if !ok {
			return fmt.Errorf("manifest missing sha256 for %s", key)
		}
		gotSize, gotHash, err := fileInfo(filepath.Join(backupDir, name))
		if err != nil {
			return fmt.Errorf("verify backup %s: %w", name, err)
		}
		if gotSize != wantSize || gotHash != wantHash {
			return fmt.Errorf("backup %s failed its integrity check (size or sha256 mismatch)", name)
		}
	}
	return nil
}

// refuseIfActive is the guard on the only destructive part of rollback: the database file may be overwritten only
// while codeplod is stopped. It never stops or kills the daemon itself.
func refuseIfActive() error {
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("systemctl is unavailable: cannot confirm codeplod is stopped, refusing to restore the database")
	}
	out, err := exec.Command(path, "is-active", "codeplod").CombinedOutput()
	if err == nil {
		return fmt.Errorf("codeplod is active (%s): stop the daemon (systemctl stop codeplod) before rollback; this command will not stop it", strings.TrimSpace(string(out)))
	}
	return nil
}

func restoreDatabase(snap, dbPath string) error {
	if err := writeFileAtomic(snap, dbPath, 0o600); err != nil {
		return err
	}
	// VACUUM INTO produces a standalone database; stale WAL/SHM files belong to the pre-rollback state and would
	// otherwise be replayed over the restored snapshot on the next boot.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
