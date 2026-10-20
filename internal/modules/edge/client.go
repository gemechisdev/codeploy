package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client talks to the Caddy admin API over its unix socket (never TCP).
type Client struct {
	Socket string
	http   *http.Client
}

func NewClient(socket string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}
	return &Client{Socket: socket, http: &http.Client{Transport: tr, Timeout: 20 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://caddy-admin"+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return b, resp.StatusCode, nil
}

// Healthy reports whether the admin API answers.
func (c *Client) Healthy(ctx context.Context) bool {
	_, code, err := c.do(ctx, http.MethodGet, "/config/", nil)
	return err == nil && code == 200
}

// Running returns the config Caddy currently serves.
func (c *Client) Running(ctx context.Context) ([]byte, error) {
	b, code, err := c.do(ctx, http.MethodGet, "/config/", nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("caddy admin: GET /config/ -> %d", code)
	}
	return b, nil
}

// Load atomically replaces the running config (Caddy validates first and keeps the old config on error).
func (c *Client) Load(ctx context.Context, cfg []byte) error {
	b, code, err := c.do(ctx, http.MethodPost, "/load", cfg)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("caddy admin: POST /load -> %d: %s", code, bytes.TrimSpace(b))
	}
	return nil
}

// Canonical re-marshals JSON through a generic value so key order and whitespace do not matter.
func Canonical(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return string(b)
	}
	o, _ := json.Marshal(v)
	return string(o)
}
