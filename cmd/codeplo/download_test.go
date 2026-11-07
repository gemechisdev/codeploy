package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStallAbortsDeadTransferAndSlowOneSucceeds(t *testing.T) {
	stallTimeout = 300 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow": // trickles bytes, total time far above the stall timeout
			for i := 0; i < 6; i++ {
				w.Write([]byte("x"))
				w.(http.Flusher).Flush()
				time.Sleep(150 * time.Millisecond)
			}
		case "/dead": // sends a byte, then goes silent
			w.Write([]byte("x"))
			w.(http.Flusher).Flush()
			time.Sleep(3 * time.Second)
		}
	}))
	defer srv.Close()
	d := t.TempDir()
	c := srv.Client()
	if err := downloadOnce(c, srv.URL+"/slow", "slow", d); err != nil {
		t.Fatalf("slow but live transfer failed: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(d, "slow")); len(b) != 6 {
		t.Fatalf("got %d bytes", len(b))
	}
	start := time.Now()
	if err := downloadOnce(c, srv.URL+"/dead", "dead", d); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("dead transfer should abort quickly, err=%v after %v", err, time.Since(start))
	}
}
