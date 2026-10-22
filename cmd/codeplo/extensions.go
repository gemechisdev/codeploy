package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/codeplo/codeplo/internal/app"
	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/modules/extensions"
)

func extensionsCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: codeplo extensions list|register|dead|check")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, key, err := app.Open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	svc := extensions.New(db, key)
	ctx := context.Background()
	switch args[0] {
	case "list":
		items, err := svc.List(ctx)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Println("no extensions registered")
			return nil
		}
		for _, e := range items {
			fmt.Printf("%s\t%s\t%s\n", e.ID, e.Name, e.Endpoint)
		}
		return nil
	case "register":
		fs := flag.NewFlagSet("extensions register", flag.ContinueOnError)
		name := fs.String("name", "", "extension name")
		endpoint := fs.String("endpoint", "", "https delivery URL")
		events := fs.String("events", "deployment.succeeded,deployment.failed", "comma-separated events")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var ev []string
		for _, e := range strings.Split(*events, ",") {
			if e = strings.TrimSpace(e); e != "" {
				ev = append(ev, e)
			}
		}
		ext, secret, err := svc.Register(ctx, *name, *endpoint, ev)
		if err != nil {
			return err
		}
		fmt.Printf("registered %s (%s)\n", ext.Name, ext.ID)
		fmt.Printf("signing secret (store once, never shown again): %s\n", secret)
		return nil
	case "dead":
		items, err := svc.DeadLetters(ctx, 50)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Println("no dead-letter deliveries")
			return nil
		}
		for _, d := range items {
			fmt.Printf("%s\t%s\t%s\tattempts=%d\t%s\n", d.ID, d.ExtensionID, d.Event, d.Attempts, d.LastError)
		}
		return nil
	case "check":
		on := svc.OutboundEnabled(ctx)
		fmt.Printf("extensions.outbound=%v\n", on)
		items, _ := svc.List(ctx)
		fmt.Printf("registered=%d\n", len(items))
		if !on {
			fmt.Fprintln(os.Stderr, "outbound delivery is off; enable feature_flags.extensions.outbound to send events")
		}
		return nil
	default:
		return fmt.Errorf("unknown extensions command %q", args[0])
	}
}
