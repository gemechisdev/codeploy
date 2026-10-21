package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// openRaw GETs a path whose response is bytes, not JSON. get() decodes JSON, so it cannot serve the archive
// download. The caller owns the returned reader; every failure path here closes the connection itself, so an
// error cannot leak one.
func (d *Docker) openRaw(ctx context.Context, p string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker"+p, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		// A bad path answers 404 with the reason in the body ("Could not find the file ...: no such file or
		// directory"), which is the only place that reason exists, so it belongs in the error. The read is capped
		// because a daemon error page can be larger than the answer.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("docker GET %s: %s %s", p, resp.Status, bytes.TrimSpace(b))
	}
	return resp.Body, nil
}

// putRaw sends a raw body, which call() cannot do because it JSON-marshals whatever it is given. The response is
// not read: extract answers 200 with an empty body and everything else is in the status putRaw reports.
func (d *Docker) putRaw(ctx context.Context, p, contentType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", "http://docker"+p, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := d.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("docker PUT %s: %s %s", p, resp.Status, bytes.TrimSpace(b))
	}
	return nil
}

// CopyFileOut returns the bytes of one regular file inside the container. It errors if the archive holds
// anything other than exactly one regular file, and refuses to read more than maxBytes so a mistaken path
// (a device, a huge log, /dev/zero) cannot exhaust the daemon's memory.
func (d *Docker) CopyFileOut(ctx context.Context, container, filePath string, maxBytes int64) ([]byte, error) {
	if err := checkID(container); err != nil {
		return nil, err
	}
	if err := checkNoNul("container path", filePath); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("docker copy out %s: the size limit must be positive, got %d", filePath, maxBytes)
	}
	r, err := d.openRaw(ctx, "/containers/"+container+"/archive?path="+url.QueryEscape(filePath))
	if err != nil {
		return nil, fmt.Errorf("docker copy out %s: %w", filePath, err)
	}
	defer r.Close()

	tr := tar.NewReader(r)
	var (
		out   []byte
		files int
	)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("docker copy out %s: reading the archive: %w", filePath, err)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			// Docker prefixes the parent directories of the requested file to the stream, and reads of a directory
			// path arrive as a whole tree of them. They carry no payload, so only the file count is guarded.
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("docker copy out %s: %s is a %s, not a regular file", filePath, hdr.Name, entryKind(hdr.Typeflag))
		}
		if files > 0 {
			return nil, fmt.Errorf("docker copy out %s: the archive holds more than one file, ask for one path", filePath)
		}
		// hdr.Size is the daemon's claim about a path the caller chose, so it is bounded by maxBytes before a single
		// byte is read. The read itself stops at hdr.Size because tar already limits a member's reader to it, which
		// keeps this arithmetic overflow-free whatever maxBytes is.
		if hdr.Size > maxBytes {
			return nil, fmt.Errorf("docker copy out %s: %s is %d bytes, over the %d byte limit", filePath, hdr.Name, hdr.Size, maxBytes)
		}
		b, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
		if err != nil {
			return nil, fmt.Errorf("docker copy out %s: reading %s: %w", filePath, hdr.Name, err)
		}
		// A short read means the stream ended mid-file: half a dump is worse than no dump, so it is an error and not
		// a smaller answer.
		if int64(len(b)) != hdr.Size {
			return nil, fmt.Errorf("docker copy out %s: %s ended after %d of %d bytes", filePath, hdr.Name, len(b), hdr.Size)
		}
		out, files = b, 1
	}
	if files == 0 {
		return nil, fmt.Errorf("docker copy out %s: the archive holds no regular file", filePath)
	}
	return out, nil
}

// CopyFileIn writes content as name inside dirPath in the container, with the given octal file mode. dirPath
// must already exist (Docker will not create intermediate directories for a file upload); the upload is a tar
// with a single regular file, as the Engine API requires.
func (d *Docker) CopyFileIn(ctx context.Context, container, dirPath, name string, mode os.FileMode, content []byte) error {
	if err := checkID(container); err != nil {
		return err
	}
	if err := checkNoNul("container path", dirPath, name); err != nil {
		return err
	}
	if !isSegment(name) {
		return fmt.Errorf("invalid file name %q: it must be a single path segment", name)
	}

	// The archive is built in memory. A dump is tens of megabytes at most, this is an operator-triggered backup
	// write rather than a hot path, and tar needs to be complete before Docker will take it, so streaming it would
	// buy a pipe pair and a goroutine to shuttle the same bytes.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: name,
		// Only the permission bits cross: mode's Go-specific flags (setuid, symlink, device, named) have no tar
		// meaning, and a backup write must not be able to plant a setuid binary in a running container.
		Mode: int64(mode.Perm()),
		// An unset mtime would restore the file dated 1970, which is what every freshness check reads as "ancient".
		ModTime:  time.Now(),
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
		// Format is left for the writer to choose: it picks USTAR and escalates to PAX only when a field will not
		// fit, so pinning PAX here would add a PaxHeaders entry the daemon never asked for.
		Format: tar.FormatUnknown,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("docker copy in %s/%s: writing the header: %w", dirPath, name, err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("docker copy in %s/%s: writing the content: %w", dirPath, name, err)
	}
	// Close is what writes the two zero blocks Docker's untar reads as the end of the archive; a skipped Close
	// uploads a truncated tar the daemon rejects with a status that says nothing about why.
	if err := tw.Close(); err != nil {
		return fmt.Errorf("docker copy in %s/%s: closing the archive: %w", dirPath, name, err)
	}

	q := url.Values{"path": []string{dirPath}}
	if err := d.putRaw(ctx, "/containers/"+container+"/archive?"+q.Encode(), "application/x-tar", buf.Bytes()); err != nil {
		return fmt.Errorf("docker copy in %s/%s: %w", dirPath, name, err)
	}
	return nil
}

// entryKind names the archive entry types a copy-out can be handed, because reporting "typeflag 57" tells an
// operator nothing about which path they pointed at.
func entryKind(t byte) string {
	switch t {
	case tar.TypeSymlink:
		return "symlink"
	case tar.TypeLink:
		return "hard link"
	case tar.TypeChar:
		return "character device"
	case tar.TypeBlock:
		return "block device"
	case tar.TypeFifo:
		return "fifo"
	default:
		return fmt.Sprintf("unsupported entry (tar type %d)", t)
	}
}

// isSegment reports whether s is a single clean path segment. The daemon untars the upload under dirPath verbatim,
// so a name holding a separator or dot-dot is how a backup file lands outside the directory that was approved.
func isSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	// A backslash is a legal filename byte on Linux, but a name that arrived from a Windows path should be refused
	// rather than created as one bizarre file next to the dump.
	if strings.ContainsAny(s, `/\`) {
		return false
	}
	return path.Clean(s) == s
}
