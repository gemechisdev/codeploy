package terminal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The daemon and the privileged PTY helper speak length-prefixed frames over a unix socket, after a one-line JSON
// handshake. One byte of type, a 4-byte big-endian length, then the payload.
const (
	FrameData   byte = 0 // PTY bytes, either direction
	FrameResize byte = 1 // daemon -> helper: cols uint16, rows uint16
	FrameExit   byte = 2 // helper -> daemon: exit code int32
	maxFrame         = 1 << 20
)

var ErrFrame = errors.New("malformed frame")

func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > maxFrame {
		return ErrFrame
	}
	hdr := make([]byte, 5)
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func ReadFrame(r *bufio.Reader) (byte, []byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("%w: %d bytes", ErrFrame, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	return hdr[0], buf, nil
}

func ResizePayload(cols, rows int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[0:], uint16(cols))
	binary.BigEndian.PutUint16(b[2:], uint16(rows))
	return b
}

func ParseResize(p []byte) (cols, rows int, err error) {
	if len(p) != 4 {
		return 0, 0, ErrFrame
	}
	return int(binary.BigEndian.Uint16(p[0:])), int(binary.BigEndian.Uint16(p[2:])), nil
}

func ExitPayload(code int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(int32(code)))
	return b
}

func ParseExit(p []byte) (int, error) {
	if len(p) != 4 {
		return 0, ErrFrame
	}
	return int(int32(binary.BigEndian.Uint32(p))), nil
}
