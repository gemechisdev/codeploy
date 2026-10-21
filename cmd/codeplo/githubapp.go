// The `codeplo github-app` commands: the App identity as the daemon sees it, and the repositories an installation
// can read.
//
// There is no route for either, and there is no need for one. The App is configured by the environment, like the
// S3 destination — its private key is not something a panel endpoint may write after boot — and the two questions
// an operator has about it are answerable from the host: do this key and these ids actually exchange for a token,
// and is the repository I am about to point a resource at inside the installation.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/modules/git/providers"
)

func githubAppCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: codeplo github-app check [--repo URL] | repos [--json]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "check":
		return githubAppCheck(rest)
	case "repos":
		return githubAppRepos(rest)
	default:
		return fmt.Errorf("unknown github-app command %q", sub)
	}
}

// loadApp reads the environment and builds the client. Every failure it returns is the operator's own
// configuration, named by the variable that needs fixing.
func loadApp() (*providers.GitHubApp, error) {
	cfg, set, err := config.LoadGitHubApp()
	if err != nil {
		return nil, err
	}
	if !set {
		return nil, fmt.Errorf("no GitHub App is configured: set CODEPLO_GITHUB_APP_ID, CODEPLO_GITHUB_APP_INSTALLATION_ID " +
			"and CODEPLO_GITHUB_APP_PRIVATE_KEY_FILE (the .pem GitHub downloaded when the App was created)")
	}
	gh, err := providers.NewGitHubApp(cfg.AppID, cfg.InstallationID, cfg.KeyPEM, cfg.APIURL)
	if err != nil {
		return nil, err
	}
	gh.WebhookSecret = cfg.WebhookSecret
	return gh, nil
}

func githubAppCheck(args []string) error {
	fs := flag.NewFlagSet("github-app check", flag.ExitOnError)
	repo := fs.String("repo", "", "also ask whether the installation covers this repository URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	gh, err := loadApp()
	if err != nil {
		return err
	}
	ctx := context.Background()
	fmt.Printf("app            %s\n", gh.AppID)
	fmt.Printf("installation   %s\n", gh.InstallationID)
	fmt.Printf("api            %s\n", gh.APIURL)
	fmt.Printf("clone host     %s\n", gh.CloneHost())
	fmt.Printf("private key    rsa-%d bits\n", gh.Key.Size()*8)
	secret := "not set (only a per-resource webhook secret can verify a delivery here)"
	if len(gh.WebhookSecret) > 0 {
		secret = "set (deliveries the App posts for any repository in the installation are verified with it)"
	}
	fmt.Printf("webhook secret %s\n", secret)

	// One failing exchange ends the check: every question below it is answered with the same token, so printing a
	// second and third failure would only bury the one that matters.
	token, err := gh.Token(ctx)
	if err != nil {
		return fmt.Errorf("the App exchanged its key for nothing: %w", err)
	}
	fmt.Printf("token          minted (%d characters)", len(token))
	if until := gh.TokenValidUntil(); until.After(time.Now().Add(30 * time.Second)) {
		fmt.Printf(", cached until %s\n", until.Format(time.RFC3339))
	} else {
		// GitHub handed back something already on its way out, which is a clock worth naming out loud: the panel can
		// still clone, but every build pays for a fresh exchange.
		fmt.Printf(", but not cached: GitHub gave it no usable expiry, so every build mints a new one\n")
	}
	list, err := gh.Repositories(ctx)
	if err != nil {
		return fmt.Errorf("the installation's repositories could not be read: %w", err)
	}
	fmt.Printf("repositories   %d readable by this installation\n", len(list))

	if *repo == "" {
		return nil
	}
	covered, err := gh.Covers(ctx, *repo)
	if err != nil {
		return err
	}
	if !covered {
		fmt.Printf("%s  is outside the installation, so a clone of it uses the resource's own credential\n", *repo)
		return fmt.Errorf("this repository is not one the App can read")
	}
	fmt.Printf("%s  the App can clone it\n", *repo)
	return nil
}

func githubAppRepos(args []string) error {
	fs := flag.NewFlagSet("github-app repos", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the installation's repositories as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	gh, err := loadApp()
	if err != nil {
		return err
	}
	list, err := gh.Repositories(context.Background())
	if err != nil {
		return err
	}
	if *asJSON {
		out, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	if len(list) == 0 {
		fmt.Println("the installation can read no repositories: grant it one on GitHub, then run this again")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY\tVISIBILITY\tDEFAULT BRANCH")
	for _, r := range list {
		visibility := "public"
		if r.Private {
			visibility = "private"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.FullName, visibility, r.DefaultBranch)
	}
	return w.Flush()
}
