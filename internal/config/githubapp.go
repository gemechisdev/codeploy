package config

import (
	"fmt"
	"os"
	"strings"
)

// GitHubApp is the panel's GitHub App identity, and it arrives entirely from the environment like the S3
// destination: the private key GitHub hands out when an App is created is a credential that has no place in a
// plaintext settings table, and a route that could write one after boot would be a way in for anyone with
// panel:admin. Nothing here is ever printed.
type GitHubApp struct {
	AppID          string
	InstallationID string
	// KeyPEM is the App's private key, either read from the file named by ..._PRIVATE_KEY_FILE or the literal PEM in
	// ..._PRIVATE_KEY. A file is the form operators actually have (GitHub downloads it as *.pem), and the inline
	// form is there for a secret store that can only hand over a value.
	KeyPEM []byte
	// APIURL is the REST base. Empty means github.com; a GitHub Enterprise Server install sets it to
	// https://host/api/v3.
	APIURL        string
	WebhookSecret []byte
}

// LoadGitHubApp reads CODEPLO_GITHUB_APP_*. The second result says whether an App was configured at all.
//
// As with the bucket, half an App is an error rather than an absence. An operator who installed the App and set
// the app id but forgot the key believes their private repositories are cloneable; starting without one would let
// the first build fail on an authentication error that reads as a permissions problem in the repository.
func LoadGitHubApp() (GitHubApp, bool, error) {
	v := GitHubApp{
		AppID:          strings.TrimSpace(os.Getenv("CODEPLO_GITHUB_APP_ID")),
		InstallationID: strings.TrimSpace(os.Getenv("CODEPLO_GITHUB_APP_INSTALLATION_ID")),
		APIURL:         strings.TrimSpace(os.Getenv("CODEPLO_GITHUB_APP_API_URL")),
	}
	file := strings.TrimSpace(os.Getenv("CODEPLO_GITHUB_APP_PRIVATE_KEY_FILE"))
	inline := os.Getenv("CODEPLO_GITHUB_APP_PRIVATE_KEY")
	secret := os.Getenv("CODEPLO_GITHUB_APP_WEBHOOK_SECRET")

	if v.AppID == "" && v.InstallationID == "" && file == "" && strings.TrimSpace(inline) == "" &&
		v.APIURL == "" && strings.TrimSpace(secret) == "" {
		return GitHubApp{}, false, nil
	}

	missing := []string{}
	if v.AppID == "" {
		missing = append(missing, "CODEPLO_GITHUB_APP_ID (the numeric id on the App's settings page)")
	}
	if v.InstallationID == "" {
		missing = append(missing, "CODEPLO_GITHUB_APP_INSTALLATION_ID (the id in the install URL: /settings/installations/<id>)")
	}
	if file == "" && strings.TrimSpace(inline) == "" {
		missing = append(missing, "CODEPLO_GITHUB_APP_PRIVATE_KEY_FILE or CODEPLO_GITHUB_APP_PRIVATE_KEY (the .pem GitHub downloaded when the App was created)")
	}
	if len(missing) > 0 {
		return GitHubApp{}, false, fmt.Errorf("the GitHub App is half-configured: set %s, or unset the CODEPLO_GITHUB_APP_* variables entirely to keep sources on deploy keys and stored tokens", strings.Join(missing, " and "))
	}

	if file != "" {
		// The file is read at boot, not per clone: a key that has been moved or has wrong permissions fails the
		// daemon where an operator is watching, rather than inside a build forty minutes later.
		pem, err := os.ReadFile(file)
		if err != nil {
			return GitHubApp{}, false, fmt.Errorf("CODEPLO_GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
		}
		v.KeyPEM = pem
	} else {
		// The inline value is not trimmed: a PEM pasted through a secret store keeps whatever trailing newline it
		// arrived with, and pem.Decode does not care.
		v.KeyPEM = []byte(inline)
	}
	if strings.TrimSpace(secret) != "" {
		v.WebhookSecret = []byte(strings.TrimSpace(secret))
	}
	return v, true, nil
}
