package edge

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"time"
)

// CertInfo is what the certificate status reader finds in Caddy's storage.
type CertInfo struct {
	Issuer   string
	NotAfter time.Time
}

// ReadCert looks for <dataDir>/certificates/<issuer>/<host>/<host>.crt (Caddy's default storage layout).
// The reader only reads files; a missing certificate is not an error (ok=false).
func ReadCert(dataDir, host string) (CertInfo, bool) {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "certificates", "*", host, host+".crt"))
	var best CertInfo
	found := false
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		blk, _ := pem.Decode(raw)
		if blk == nil {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			continue
		}
		if !found || c.NotAfter.After(best.NotAfter) {
			iss := c.Issuer.CommonName
			if iss == "" && len(c.Issuer.Organization) > 0 {
				iss = c.Issuer.Organization[0]
			}
			best, found = CertInfo{Issuer: iss, NotAfter: c.NotAfter}, true
		}
	}
	return best, found
}
