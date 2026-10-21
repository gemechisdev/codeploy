// The `codeplo cron` commands: K2's scheduled tasks, operated from a terminal.
//
// There is no cron route in the contract, so this is the only way to write a job, and it is deliberately the same
// code path the daemon runs: the argv is validated by internal/modules/cron before it is stored, which means a job
// that types here is a job that will execute there.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/codeplo/codeplo/internal/app"
	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/modules/cron"
)

// cronCommand opens the data directory itself, because `run` dispatches cron before its own config load: the argv
// has to survive to the sub-command, and the only way to guarantee that is to not parse it twice.
func cronCommand(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, key, err := app.Open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	return cronCmd(context.Background(), db, key, args)
}

func cronCmd(ctx context.Context, db *sql.DB, key []byte, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: codeplo cron list | add --resource R --name N --cron EXPR [--timeout S] -- argv... | remove <id|name> | history <id|name> [--limit N]")
	}
	svc := cron.New(db, key)
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return cronList(ctx, svc, rest)
	case "add":
		return cronAdd(ctx, svc, rest)
	case "remove":
		return cronRemove(ctx, svc, rest)
	case "history":
		return cronHistory(ctx, svc, rest)
	default:
		return fmt.Errorf("unknown cron command %q", sub)
	}
}

func cronList(ctx context.Context, svc *cron.Service, args []string) error {
	fs := flag.NewFlagSet("cron list", flag.ExitOnError)
	_ = fs.Parse(args)
	list, err := svc.List(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no cron jobs")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tRESOURCE\tNAME\tCRON\tTIMEOUT\tSTATUS\tLAST RUN\tLAST RESULT")
	for _, j := range list {
		enabled := "disabled"
		if j.Enabled {
			enabled = "enabled"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%ds\t%s\t%s\t%s\n", j.ID, j.ResourceID, j.Name, j.Cron, j.TimeoutS, enabled,
			orDash(j.LastRunAt), orDash(j.LastStatus))
	}
	return w.Flush()
}

// cronAdd stores a job. Everything after `--` is the argv, so a command with flags needs no quoting and no shell:
//
//	codeplo cron add --resource api --name "cleanup" --cron "0 3 * * *" -- find /data -mtime +30 -delete
func cronAdd(ctx context.Context, svc *cron.Service, args []string) error {
	fs := flag.NewFlagSet("cron add", flag.ExitOnError)
	resource := fs.String("resource", "", "resource id or name to run inside")
	name := fs.String("name", "", "job name")
	expr := fs.String("cron", "", "5-field cron expression")
	timeout := fs.Int("timeout", 600, "kill the command after this many seconds")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The flag package stops at a bare `--`, so everything after it — flags and all — arrives here untouched and
	// becomes the argv the container executes.
	argv := fs.Args()
	j, err := svc.Add(ctx, cron.AddInput{ResourceNameOrID: *resource, Name: *name, Cron: *expr, Command: argv, TimeoutS: *timeout})
	if err != nil {
		var f cron.FieldErrors
		if errors.As(err, &f) {
			keys := make([]string, 0, len(f))
			for k := range f {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			msgs := make([]string, 0, len(keys))
			for _, k := range keys {
				msgs = append(msgs, k+": "+f[k])
			}
			return fmt.Errorf("%s", strings.Join(msgs, "; "))
		}
		return err
	}
	fmt.Printf("added %s (%s) to %s: %v\n", j.Name, j.ID, j.ResourceID, j.Command)
	return nil
}

func cronRemove(ctx context.Context, svc *cron.Service, args []string) error {
	fs := flag.NewFlagSet("cron remove", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: codeplo cron remove <id|name>")
	}
	if err := svc.Remove(ctx, fs.Arg(0)); err != nil {
		return err
	}
	fmt.Printf("removed cron job %s and its history\n", fs.Arg(0))
	return nil
}

func cronHistory(ctx context.Context, svc *cron.Service, args []string) error {
	fs := flag.NewFlagSet("cron history", flag.ExitOnError)
	limit := fs.Int("limit", 20, "how many attempts to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: codeplo cron history <id|name> [--limit N]")
	}
	runs, err := svc.History(ctx, fs.Arg(0), *limit)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Println("this job has never run")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "AT\tSTATUS\tEXIT\tDURATION\tERROR")
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%s\t%d\t%dms\t%s\n", r.At, r.Status, r.ExitCode, r.DurationMS, r.Error)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// The output tail goes after the table rather than inside it: it is the multi-line part, and putting it in a
	// column would wrap every row into an unreadable block.
	last := runs[0]
	if strings.TrimSpace(last.Output) != "" {
		fmt.Printf("\noutput of %s (%s):\n%s\n", last.At, last.Status, last.Output)
	}
	return nil
}

func orDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}
