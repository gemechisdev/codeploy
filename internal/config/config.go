// Package config: environment/flag parsing and validation. The panel only ever binds loopback and a unix socket.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type Config struct {
	DataDir    string
	Listen     string // 127.0.0.1:port; the loopback listener is mandatory (break-glass) and cannot be disabled
	SocketPath string // unix socket used by Caddy
	// TemplatesDir is where an operator drops Compose template files to add them to the catalog. It is read on
	// every catalog list, so an import needs no restart.
	TemplatesDir string
}

func (c Config) DBPath() string        { return filepath.Join(c.DataDir, "codeplo.db") }
func (c Config) MasterKeyPath() string { return filepath.Join(c.DataDir, "master.key") }

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func Load() (Config, error) {
	c := Config{
		DataDir: getenv("CODEPLO_DATA_DIR", "/var/lib/codeplo"),
		Listen:  getenv("CODEPLO_LISTEN", "127.0.0.1:7443"),
	}
	c.SocketPath = getenv("CODEPLO_SOCKET", filepath.Join(c.DataDir, "codeplo.sock"))
	c.TemplatesDir = getenv("CODEPLO_TEMPLATES_DIR", filepath.Join(c.DataDir, "templates"))
	return c, c.Validate()
}

// Validate refuses any non-loopback TCP bind.
func (c Config) Validate() error {
	h, _, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("CODEPLO_LISTEN: %w", err)
	}
	if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("CODEPLO_LISTEN must be a loopback address (got %q): the panel is never bound publicly", h)
	}
	return nil
}
