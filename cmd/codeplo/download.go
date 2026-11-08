package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Release downloads have to work on slow, flaky links (a few hundred bytes per second is real) the way the
// installer's curl does: show progress, keep what has arrived, retry, and resume rather than start over.
//
//   - Each file streams into NAME.part and is renamed only when complete, so a NAME on disk is always whole.
//   - A retry, or the next run, resumes with a Range request. If the server sent an ETag/Last-Modified it is saved
//     and sent as If-Range, so a release that changed in between is fetched whole instead of being spliced.
//   - The wait for the first byte (DNS, TLS, GitHub's redirect to its CDN) is generous; once data flows, only a
//     transfer that then delivers nothing for stallTimeout is abandoned.
//   - Permanent client errors (404 and friends) are not retried.
var (
	stallTimeout             = 60 * time.Second  // no body bytes for this long: give up on the attempt
	headerTimeout            = 180 * time.Second // time allowed for connect + redirects + response headers
	maxAttempts              = 5
	retryBase                = 2 * time.Second // doubled per attempt, capped at retryCap
	retryCap                 = 30 * time.Second
	progressStderr io.Writer = os.Stderr
)

func newDownloadClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
	}}
}

type httpStatusError struct {
	name string
	code int
}

func (e httpStatusError) Error() string { return fmt.Sprintf("download %s: HTTP %d", e.name, e.code) }

// permanent reports an error a retry cannot fix.
func permanent(err error) bool {
	var he httpStatusError
	if errors.As(err, &he) {
		return he.code >= 400 && he.code < 500 && he.code != 408 && he.code != 429 && he.code != 416
	}
	return false
}

// ---- progress ----

type meter struct {
	w          io.Writer
	tty        bool
	name       string
	total      int64 // -1 when unknown
	base       int64 // bytes already on disk when this attempt started
	done       int64
	start      time.Time
	lastPrint  time.Time
	lastBucket int
	now        func() time.Time
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func newMeter(w io.Writer, name string, total, base int64) *meter {
	m := &meter{w: w, tty: isTerminal(w), name: name, total: total, base: base, done: base, now: time.Now, lastBucket: -1}
	m.start = m.now()
	return m
}

func humanBytes(n int64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k:
		return fmt.Sprintf("%.1f GiB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.1f MiB", f/(k*k))
	case f >= k:
		return fmt.Sprintf("%.0f KiB", f/k)
	}
	return fmt.Sprintf("%d B", n)
}

func humanDuration(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s >= 3600:
		return fmt.Sprintf("%dh%02dm", s/3600, s%3600/60)
	case s >= 60:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%ds", s)
}

// line renders one status line: name, bar, percent, sizes, speed of this attempt, and ETA.
func (m *meter) line() string {
	elapsed := m.now().Sub(m.start).Seconds()
	speed := 0.0
	if elapsed > 0 {
		speed = float64(m.done-m.base) / elapsed
	}
	if m.total <= 0 {
		return fmt.Sprintf("  %-26s %s  %s/s", m.name, humanBytes(m.done), humanBytes(int64(speed)))
	}
	pct := int(float64(m.done) * 100 / float64(m.total))
	if pct > 100 {
		pct = 100
	}
	const width = 20
	filled := pct * width / 100
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	eta := "--"
	if speed > 0 && m.done < m.total {
		eta = humanDuration(time.Duration(float64(m.total-m.done)/speed) * time.Second)
	}
	return fmt.Sprintf("  %-26s [%s] %3d%%  %s/%s  %s/s  ETA %s", m.name, bar, pct, humanBytes(m.done), humanBytes(m.total), humanBytes(int64(speed)), eta)
}

// add records n more bytes and prints. A terminal gets one line rewritten about five times a second; a log gets a
// new line at each 25% step or every 15 seconds, so redirected output stays readable.
func (m *meter) add(n int) {
	m.done += int64(n)
	now := m.now()
	if m.tty {
		if now.Sub(m.lastPrint) >= 200*time.Millisecond {
			m.lastPrint = now
			fmt.Fprintf(m.w, "\r\033[K%s", m.line())
		}
		return
	}
	bucket := -1
	if m.total > 0 {
		bucket = int(m.done*4/m.total) * 25
	}
	if bucket != m.lastBucket || now.Sub(m.lastPrint) >= 15*time.Second {
		m.lastBucket, m.lastPrint = bucket, now
		fmt.Fprintln(m.w, m.line())
	}
}

func (m *meter) finish() {
	if m.tty {
		fmt.Fprintf(m.w, "\r\033[K%s\n", m.line())
		return
	}
	fmt.Fprintln(m.w, m.line())
}

func (m *meter) abort() {
	if m.tty {
		fmt.Fprint(m.w, "\n")
	}
}

type progressReader struct {
	r io.Reader
	t *time.Timer
	m *meter
}

func (p progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.t.Reset(stallTimeout)
		p.m.add(n)
	}
	return n, err
}

// ---- download ----

// downloadFile fetches base/name into dir, resuming and retrying. A file already present in dir is whole (it is
// only ever renamed into place when complete) and is kept.
func downloadFile(client *http.Client, base, name, dir string) error {
	u := strings.TrimRight(base, "/") + "/" + name
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "https" {
		return fmt.Errorf("refusing non-https download URL %q", u)
	}
	return downloadURL(client, u, name, dir)
}

func downloadURL(client *http.Client, u, name, dir string) error {
	final := filepath.Join(dir, name)
	if st, err := os.Stat(final); err == nil && st.Mode().IsRegular() {
		fmt.Fprintf(progressStderr, "  %-26s already downloaded\n", name)
		return nil
	}
	var last error
	delay := retryBase
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			fmt.Fprintf(progressStderr, "  retrying %s in %s (attempt %d/%d); the partial file is kept\n", name, humanDuration(delay), attempt, maxAttempts)
			time.Sleep(delay)
			if delay *= 2; delay > retryCap {
				delay = retryCap
			}
		}
		if last = downloadOnce(client, u, name, dir); last == nil {
			return nil
		}
		fmt.Fprintf(progressStderr, "  failed: %v\n", last)
		if permanent(last) {
			return last
		}
	}
	return fmt.Errorf("%w (re-run to resume from the partial file)", last)
}

func partPaths(dir, name string) (part, validator string) {
	return filepath.Join(dir, name+".part"), filepath.Join(dir, name+".part.validator")
}

func downloadOnce(client *http.Client, u, name, dir string) error {
	part, valPath := partPaths(dir, name)
	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}
	var stalled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(headerTimeout, func() { stalled.Store(true); cancel() })
	defer timer.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if b, err := os.ReadFile(valPath); err == nil && len(b) > 0 {
			req.Header.Set("If-Range", strings.TrimSpace(string(b)))
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		if stalled.Load() {
			return fmt.Errorf("download %s: no response within %s", name, headerTimeout)
		}
		return fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	timer.Reset(stallTimeout)

	total := int64(-1)
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, tot, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != offset {
			_ = os.Remove(part)
			return fmt.Errorf("download %s: server answered a different range; restarting", name)
		}
		total = tot
	case http.StatusOK:
		offset = 0 // the server ignored Range or the file changed: start over
		total = resp.ContentLength
	case http.StatusRequestedRangeNotSatisfiable:
		// The partial file is as long as the whole file, or longer than a changed one.
		if _, tot, ok := parseContentRange(resp.Header.Get("Content-Range")); ok && tot == offset {
			return finishDownload(part, valPath, filepath.Join(dir, name))
		}
		_ = os.Remove(part)
		return httpStatusError{name, resp.StatusCode}
	default:
		return httpStatusError{name, resp.StatusCode}
	}
	if v := resp.Header.Get("ETag"); v != "" && !strings.HasPrefix(v, "W/") {
		_ = os.WriteFile(valPath, []byte(v), 0o600)
	} else if v := resp.Header.Get("Last-Modified"); v != "" {
		_ = os.WriteFile(valPath, []byte(v), 0o600)
	} else {
		_ = os.Remove(valPath)
	}

	flags := os.O_WRONLY | os.O_CREATE
	if offset > 0 {
		flags |= os.O_APPEND
		fmt.Fprintf(progressStderr, "  %-26s resuming at %s\n", name, humanBytes(offset))
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return err
	}
	m := newMeter(progressStderr, name, total, offset)
	n, err := io.Copy(f, progressReader{r: resp.Body, t: timer, m: m})
	cerr := f.Close()
	if err != nil {
		m.abort()
		if stalled.Load() {
			return fmt.Errorf("download %s: no data for %s after %s", name, stallTimeout, humanBytes(offset+n))
		}
		return fmt.Errorf("download %s: %w after %s", name, err, humanBytes(offset+n))
	}
	if cerr != nil {
		return cerr
	}
	if total >= 0 && offset+n != total {
		m.abort()
		return fmt.Errorf("download %s: connection closed after %s of %s", name, humanBytes(offset+n), humanBytes(total))
	}
	m.finish()
	return finishDownload(part, valPath, filepath.Join(dir, name))
}

func finishDownload(part, valPath, final string) error {
	_ = os.Remove(valPath)
	return os.Rename(part, final)
}

// parseContentRange reads "bytes START-END/TOTAL" and "bytes */TOTAL" (start is -1 for the latter, with ok true).
func parseContentRange(h string) (start, total int64, ok bool) {
	h = strings.TrimSpace(strings.TrimPrefix(h, "bytes "))
	slash := strings.IndexByte(h, '/')
	if slash < 0 {
		return 0, 0, false
	}
	tot, err := strconv.ParseInt(h[slash+1:], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	span := h[:slash]
	if span == "*" {
		return -1, tot, true
	}
	dash := strings.IndexByte(span, '-')
	if dash < 0 {
		return 0, 0, false
	}
	s, err := strconv.ParseInt(span[:dash], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return s, tot, true
}
