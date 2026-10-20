package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestDemuxSplitsStreamsAndJoinsPartialLines(t *testing.T) {
	var in bytes.Buffer
	in.Write(frame(1, "2026-10-21T10:00:00.000000001Z hello\n2026-10-21T10:00:01Z par"))
	in.Write(frame(2, "2026-10-21T10:00:02Z oops\n"))
	in.Write(frame(1, "tial\n"))
	var got []LogLine
	if err := demux(&in, func(l LogLine) error { got = append(got, l); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Line != "hello" || got[0].Stream != "stdout" || got[1].Stream != "stderr" || got[2].Line != "partial" {
		t.Fatalf("unexpected lines: %+v", got)
	}
	if got[0].Time.Nanosecond() != 1 || got[0].Time.Year() != 2026 {
		t.Fatalf("timestamp not parsed: %v", got[0].Time)
	}
}

func TestDemuxBackpressureStopsOnEmitError(t *testing.T) {
	var in bytes.Buffer
	for i := 0; i < 5; i++ {
		in.Write(frame(1, "2026-10-21T10:00:00Z x\n"))
	}
	stop := errors.New("slow consumer")
	n := 0
	err := demux(&in, func(LogLine) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 2 {
		t.Fatalf("emit error must stop the stream: n=%d err=%v", n, err)
	}
}

func TestDemuxRejectsHugeFrame(t *testing.T) {
	h := make([]byte, 8)
	h[0] = 1
	binary.BigEndian.PutUint32(h[4:], 1<<30)
	if err := demux(bytes.NewReader(h), func(LogLine) error { return nil }); err == nil {
		t.Fatal("a 1 GiB frame must be refused")
	}
}

func TestEmitLineWithoutTimestamp(t *testing.T) {
	var l LogLine
	emitLine([]byte("no timestamp here"), "stdout", func(x LogLine) error { l = x; return nil })
	if l.Line != "no timestamp here" || !l.Time.IsZero() {
		t.Fatalf("%+v", l)
	}
}

func TestIDValidation(t *testing.T) {
	d := NewDocker("/nonexistent.sock")
	if _, err := d.Resolve(context.Background(), "a\"],\"status\":[\"x"); !errors.Is(err, ErrNoContainer) {
		t.Fatalf("a hostile resource id must never reach the filter JSON: %v", err)
	}
	if err := d.Logs(context.Background(), "../../etc", LogOptions{}, nil); !errors.Is(err, ErrNoContainer) {
		t.Fatalf("a non-hex container id must be refused: %v", err)
	}
}
