package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/codeplo/codeplo/internal/app"
	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/modules"
	"github.com/codeplo/codeplo/internal/modules/previews"
)

// previewsCommand is `codeplo previews list [--json] | check`: what this host is running for pull requests, and which
// of the switches that make previews happen is still off. It reads the database and the environment and writes
// nothing — taking a preview down is the sweep's job, on the daemon that owns the containers.
func previewsCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: codeplo previews list [--json] | check [--json]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, _, err := app.Open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	// No engine and no queue: this is a reader. The two are what the daemon wires, and `check` says which module
	// provides them rather than pretending to have them, because a verdict from a process that could never build a
	// preview is a fact about the reader and not about the panel.
	svc := previews.New(previews.Deps{DB: db, BaseDomain: config.PreviewBaseDomain(), Now: time.Now})
	ctx := context.Background()
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return previewsList(ctx, svc, rest)
	case "check":
		return previewsCheck(ctx, db, svc, rest)
	default:
		return fmt.Errorf("unknown previews command %q", sub)
	}
}

func previewsList(ctx context.Context, svc *previews.Service, args []string) error {
	fs := flag.NewFlagSet("previews list", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the rows as the panel stores them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	list, err := svc.Rows(ctx)
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
		fmt.Println("no preview environments are running")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVICE\tPR\tBRANCH\tURL\tEXPIRES\tSTATE")
	for _, r := range list {
		fmt.Fprintf(w, "%s\t#%d\t%s\t%s\t%s\t%s\n", r.ParentSlug, r.Number, r.HeadRef, r.URL, r.ExpiresAt, previewState(r))
	}
	return w.Flush()
}

// previewState is the one thing a list of hostnames cannot say on its own: whether the workload behind the row is
// still there. `gone` means an operator deleted the preview from its resource page and the sweep has its bookkeeping
// to finish; `due` means the clock has run out and the next minute takes it down.
func previewState(r previews.Row) string {
	switch {
	case r.ResourceGone:
		return "gone"
	case r.ExpiresAt <= time.Now().UTC().Format("2006-01-02T15:04:05.000Z"):
		return "due"
	default:
		return "live"
	}
}

func previewsCheck(ctx context.Context, db *sql.DB, svc *previews.Service, args []string) error {
	fs := flag.NewFlagSet("previews check", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the status as one JSON object")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st := svc.Check(ctx)
	deployOn := modules.NewRegistry(db).Enabled(ctx, "deploy")
	broken := []string{}
	if st.FlagOn {
		// Only an operator who has asked for previews can be told their previews are not working. With the flag off
		// the panel is doing exactly what it was told.
		if !st.DomainReady {
			broken = append(broken, st.DomainNote)
		}
		if !deployOn {
			broken = append(broken, "The deploy module is off, so the daemon has no engine to build a preview with")
		}
	}
	// The exit is the CI-usable part: a panel with the flag on and no usable domain is a branch nobody is going to
	// see a preview of, and that is worth failing a check for.
	refused := fmt.Errorf("preview environments are on and %d thing(s) are missing", len(broken))
	if *asJSON {
		out, err := json.MarshalIndent(map[string]any{
			"status": st, "deploy_module": deployOn, "problems": broken,
		}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		if len(broken) > 0 {
			return refused
		}
		return nil
	}
	fmt.Printf("base domain      %s\n", orNone(st.BaseDomain))
	if !st.DomainReady {
		// The value is read from this shell and the daemon reads its own, so an empty answer here is about where the
		// command was run as often as it is about the host's configuration. Saying which is the difference between
		// "not configured" and "configured, and you are looking from a shell that cannot see it".
		fmt.Printf("                 %s\n", st.DomainNote)
		if st.BaseDomain == "" {
			fmt.Println("                 this command reads CODEPLO_PREVIEW_DOMAIN from its own environment; the daemon reads")
			fmt.Println("                 its service environment (EnvironmentFile=-/etc/default/codeplod)")
		}
	}
	fmt.Printf("feature flag     %s (%s)\n", onOff(st.FlagOn), "git.preview_deploys")
	fmt.Printf("deploy module    %s\n", onOff(deployOn))
	fmt.Printf("lifetime         %dh of inactivity\n", st.TTLHours)
	fmt.Printf("per service      %d previews at once\n", st.MaxPerService)
	fmt.Printf("memory cap       %d MB per preview\n", st.MemoryMB)
	fmt.Printf("live previews    %d\n", st.Live)
	for _, p := range broken {
		fmt.Println("problem:", p)
	}
	if !st.FlagOn {
		fmt.Println("previews are off: nothing is built for a pull request until the git.preview_deploys flag is on")
		return nil
	}
	if len(broken) > 0 {
		return refused
	}
	fmt.Println("previews are ready")
	return nil
}

func orNone(v string) string {
	if v == "" {
		return "(not set)"
	}
	return v
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
