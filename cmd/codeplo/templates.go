// The `codeplo templates` commands: the catalog as the panel sees it, including the files it refused to list.
//
// There is no route for an import, and there is no need for one: a template is a file in the data directory. What
// a file has to earn is a place in the catalog, so `check` is the command that says whether it did and why not —
// the one thing an operator cannot learn from the panel, because a template that is not deployable is not listed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/modules/templates"
)

func templatesCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: codeplo templates list [--json] | check")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	sub, rest := args[0], args[1:]
	catalog := &templates.Catalog{Dir: cfg.TemplatesDir}
	switch sub {
	case "list":
		return templatesList(catalog, rest)
	case "check":
		return templatesCheck(catalog, cfg.TemplatesDir, rest)
	default:
		return fmt.Errorf("unknown templates command %q", sub)
	}
}

func templatesList(catalog *templates.Catalog, args []string) error {
	fs := flag.NewFlagSet("templates list", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the catalog as the API returns it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, _ := catalog.List()
	if *asJSON {
		out, err := json.MarshalIndent(items, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
		return nil
	}
	if len(items) == 0 {
		fmt.Println("the catalog is empty: run codeplo templates check to see why")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tNAME\tKIND\tCATEGORY\tPORT\tMEMORY\tCREDENTIALS GENERATED")
	for _, t := range items {
		secrets := []string{}
		for _, v := range t.Variables {
			if v.Secret {
				secrets = append(secrets, v.Name)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d MB\t%s\n", t.Key, t.Name, t.Kind, t.Category, t.DefaultPort,
			t.RecommendedMemoryMB, strings.Join(secrets, ", "))
	}
	return w.Flush()
}

// templatesCheck lists what the panel will offer and prints a reason for everything it will not, which is the only
// way a dropped-in file can be debugged.
func templatesCheck(catalog *templates.Catalog, dir string, args []string) error {
	fs := flag.NewFlagSet("templates check", flag.ExitOnError)
	_ = fs.Parse(args)
	items, issues := catalog.List()
	fmt.Printf("catalog directory: %s\n", dir)
	if _, err := os.Stat(dir); err != nil {
		fmt.Println("(it does not exist yet: templates dropped there are added to the panel without a restart)")
	}
	fmt.Printf("%d template(s) deployable:\n", len(items))
	for _, t := range items {
		fmt.Printf("  %s\n", t.Summary())
		for _, w := range t.Warnings {
			fmt.Printf("      note: %s\n", w)
		}
	}
	if len(issues) == 0 {
		fmt.Println("nothing was refused")
		return nil
	}
	fmt.Printf("\n%d file(s) not in the catalog:\n", len(issues))
	for _, i := range issues {
		fmt.Printf("  %s\n      %s\n", i.File, i.Reason)
	}
	return fmt.Errorf("%d template file(s) could not be listed", len(issues))
}
