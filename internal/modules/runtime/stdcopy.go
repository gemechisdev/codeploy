package runtime

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// maxFrameBytes is the largest stdout/stderr chunk Docker's framing is allowed to declare. The daemon writes at
// most 32 KiB per frame, so a bigger value means the stream is not framed at all and the bytes are being read as
// a header. Without the guard, four bytes of coincidence can name a gigabyte-sized payload.
const maxFrameBytes = 4 << 20

// frameChunkBytes bounds the allocation used to shuttle a frame's payload to its writer.
const frameChunkBytes = 64 << 10

// CopyDemuxed splits Docker's multiplexed non-tty stream into stdout and stderr, writing stdout bytes to stdout
// and stderr bytes to stderr, and returns how many bytes landed in stdout.
//
// This is the binary-safe sibling of demux in docker.go. That one exists for log lines: it reassembles lines and
// strips the timestamp the daemon prefixes them with, which is correct for text and corrupts a database dump the
// moment a payload happens to contain a newline. A dump must therefore be read through here.
//
// The wire format is an 8-byte header per chunk (stream type, three zero bytes, payload length as a big-endian
// uint32) followed by that many payload bytes. A frame may end mid-value; the type is what tells the two streams
// apart, not any byte inside the payload.
//
// Reaching the end of the stream is success: a command that produced no output leaves nothing after its last
// frame, and an io.EOF (or a partial header, which is a hung-up connection mid-nothing) is how that reads.
func CopyDemuxed(r io.Reader, stdout, stderr io.Writer) (int64, error) {
	hdr := make([]byte, 8)
	chunk := make([]byte, frameChunkBytes)
	var written int64
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return written, nil
			}
			return written, err
		}
		var out io.Writer
		switch hdr[0] {
		case 1:
			out = stdout
		case 2:
			out = stderr
		default:
			return written, fmt.Errorf("docker stream: frame marked for stream %d, expected 1 (stdout) or 2 (stderr) - the stream is not multiplexed", hdr[0])
		}
		if hdr[1] != 0 || hdr[2] != 0 || hdr[3] != 0 {
			return written, errors.New("docker stream: multiplexed frame header is malformed")
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		if n > maxFrameBytes {
			return written, fmt.Errorf("docker stream: frame of %d bytes over the %d limit", n, maxFrameBytes)
		}
		for remaining := n; remaining > 0; {
			m := remaining
			if m > frameChunkBytes {
				m = frameChunkBytes
			}
			if _, err := io.ReadFull(r, chunk[:m]); err != nil {
				return written, err
			}
			if _, err := out.Write(chunk[:m]); err != nil {
				return written, err
			}
			if hdr[0] == 1 {
				written += m
			}
			remaining -= m
		}
	}
}
