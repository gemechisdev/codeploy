package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fast(t *testing.T) *bytes.Buffer {
	t.Helper()
	oldStall, oldHeader, oldBase, oldCap, oldOut, oldMax := stallTimeout, headerTimeout, retryBase, retryCap, progressStderr, maxAttempts
	var buf bytes.Buffer
	stallTimeout, headerTimeout, retryBase, retryCap, progressStderr, maxAttempts = 400*time.Millisecond, 2*time.Second, 5*time.Millisecond, 20*time.Millisecond, &buf, 4
	t.Cleanup(func() {
		stallTimeout, headerTimeout, retryBase, retryCap, progressStderr, maxAttempts = oldStall, oldHeader, oldBase, oldCap, oldOut, oldMax
	})
	return &buf
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

type recorder struct {
	mu     sync.Mutex
	ranges []string
	count  int
}

func (r *recorder) note(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
	r.ranges = append(r.ranges, req.Header.Get("Range"))
}

func TestDownloadFreshShowsProgressAndLeavesNoPartFile(t *testing.T) {
	out := fast(t)
	data := payload(200_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := downloadURL(srv.Client(), srv.URL+"/f.bin", "f.bin", dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.bin"))
	if !bytes.Equal(got, data) {
		t.Fatal("content differs")
	}
	for _, leftover := range []string{"f.bin.part", "f.bin.part.validator"} {
		if _, err := os.Stat(filepath.Join(dir, leftover)); err == nil {
			t.Errorf("%s left behind", leftover)
		}
	}
	if !strings.Contains(out.String(), "100%") || !strings.Contains(out.String(), "f.bin") {
		t.Errorf("no progress output:\n%s", out)
	}
	// A file that is already whole is kept, not fetched again.
	if err := downloadURL(srv.Client(), "https://invalid.invalid/f.bin", "f.bin", dir); err != nil {
		t.Errorf("existing complete file should be skipped: %v", err)
	}
}

func TestDownloadResumesWithRangeAndIfRange(t *testing.T) {
	out := fast(t)
	data := payload(100_000)
	rec := &recorder{}
	var ifRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.note(r)
		ifRange = r.Header.Get("If-Range")
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	part, val := partPaths(dir, "f.bin")
	os.WriteFile(part, data[:40_000], 0o600)
	os.WriteFile(val, []byte(`"v1"`), 0o600)
	if err := downloadURL(srv.Client(), srv.URL+"/f.bin", "f.bin", dir); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.bin"))
	if !bytes.Equal(got, data) {
		t.Fatalf("resumed file differs (%d bytes)", len(got))
	}
	if rec.ranges[0] != "bytes=40000-" || ifRange != `"v1"` {
		t.Errorf("Range=%q If-Range=%q", rec.ranges[0], ifRange)
	}
	if !strings.Contains(out.String(), "resuming at") {
		t.Errorf("resume not announced:\n%s", out)
	}
}

func TestDownloadChangedReleaseIsFetchedWholeNotSpliced(t *testing.T) {
	fast(t)
	data := payload(60_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v2"`) // the release moved on
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	part, val := partPaths(dir, "f.bin")
	os.WriteFile(part, bytes.Repeat([]byte("OLD"), 5000), 0o600)
	os.WriteFile(val, []byte(`"v1"`), 0o600)
	if err := downloadURL(srv.Client(), srv.URL+"/f.bin", "f.bin", dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f.bin")); !bytes.Equal(got, data) {
		t.Fatal("a partial file from another version was spliced into the new one")
	}
}

func TestDownloadStallKeepsPartialAndResumes(t *testing.T) {
	out := fast(t)
	data := payload(80_000)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.note(r)
		w.Header().Set("ETag", `"v1"`)
		if rec.count == 1 { // first attempt: send half, then go silent
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.WriteHeader(200)
			w.Write(data[:30_000])
			w.(http.Flusher).Flush()
			time.Sleep(3 * time.Second)
			return
		}
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	dir := t.TempDir()
	start := time.Now()
	if err := downloadURL(srv.Client(), srv.URL+"/f.bin", "f.bin", dir); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "f.bin")); !bytes.Equal(got, data) {
		t.Fatal("content differs after resume")
	}
	if rec.count < 2 || rec.ranges[1] != "bytes=30000-" {
		t.Errorf("second attempt did not resume: %v", rec.ranges)
	}
	if time.Since(start) > 2500*time.Millisecond {
		t.Errorf("stall was not detected quickly: %v", time.Since(start))
	}
	if !strings.Contains(out.String(), "no data for") {
		t.Errorf("stall not explained:\n%s", out)
	}
}

func TestDownloadSlowButLiveTransferSucceeds(t *testing.T) {
	fast(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 8; i++ { // well past stallTimeout in total, but never silent for that long
			w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	if err := downloadURL(srv.Client(), srv.URL+"/s.bin", "s.bin", dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "s.bin")); len(b) != 8 {
		t.Fatalf("got %d bytes", len(b))
	}
}

func TestDownloadDoesNotRetryAPermanentError(t *testing.T) {
	fast(t)
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.note(r)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	err := downloadURL(srv.Client(), srv.URL+"/missing", "missing", t.TempDir())
	if err == nil || rec.count != 1 {
		t.Fatalf("404 must fail once; err=%v requests=%d", err, rec.count)
	}
	// A server error is transient and is retried.
	rec2 := &recorder{}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec2.note(r); w.WriteHeader(503) }))
	defer srv2.Close()
	if err := downloadURL(srv2.Client(), srv2.URL+"/x", "x", t.TempDir()); err == nil || rec2.count != maxAttempts {
		t.Fatalf("503 should use all %d attempts; err=%v requests=%d", maxAttempts, err, rec2.count)
	}
}

func TestDownloadRefusesPlainHTTP(t *testing.T) {
	if err := downloadFile(nil, "http://example.com", "f", t.TempDir()); err == nil {
		t.Fatal("must refuse non-https")
	}
}

func TestMeterLinesAndFormatting(t *testing.T) {
	var buf bytes.Buffer
	m := newMeter(&buf, "codeplod_linux_amd64", 4_000_000, 0)
	base := time.Unix(1000, 0)
	m.start, m.now = base, func() time.Time { return base.Add(10 * time.Second) }
	m.done = 1_000_000
	line := m.line()
	for _, want := range []string{"codeplod_linux_amd64", "25%", "977 KiB/3.8 MiB", "98 KiB/s", "ETA 30s"} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %q: %s", want, line)
		}
	}
	// Redirected output logs at 25% steps rather than flooding.
	m2 := newMeter(&buf, "f", 1000, 0)
	for i := 0; i < 100; i++ {
		m2.add(10)
	}
	if n := strings.Count(buf.String(), "\n"); n > 8 {
		t.Errorf("too many log lines for a non-terminal: %d\n%s", n, buf.String())
	}
	for in, want := range map[int64]string{500: "500 B", 2048: "2 KiB", 5 << 20: "5.0 MiB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %s want %s", in, got, want)
		}
	}
	if c, tot, ok := parseContentRange("bytes 10-99/100"); !ok || c != 10 || tot != 100 {
		t.Error("content-range parse")
	}
	if c, tot, ok := parseContentRange("bytes */100"); !ok || c != -1 || tot != 100 {
		t.Error("unsatisfied content-range parse")
	}
}
