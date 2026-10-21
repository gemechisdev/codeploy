package config

import (
	"os"
	"strings"
)

// PreviewBaseDomain is CODEPLO_PREVIEW_DOMAIN: the suffix every pull-request preview hostname is built under, such
// as previews.example.com. It is environment rather than a settings row for the same reason the App's identity is:
// the value is only usable once someone has put a wildcard record (preview.example.com, one label deep) pointing at
// this host, and an operator who changed it in the panel without changing the record would get certificates that
// cannot be issued rather than a setting that saved them from that.
//
// Nothing is validated here, because an unusable domain is not a boot failure — the panel has routes, workloads and
// a UI to serve whether previews can name anything or not. The previews service says which of the two it is missing,
// the daemon's boot log carries that sentence, and `codeplo previews check` answers it on request.
func PreviewBaseDomain() string {
	return strings.TrimSpace(os.Getenv("CODEPLO_PREVIEW_DOMAIN"))
}
