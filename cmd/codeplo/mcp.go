// The `codeplo mcp` commands: read-only state of the daemon-local MCP endpoint (T53/T59).
//
// MCP has no HTTP route, so an operator's only view of it is this host-side check. cmd/codeplo/main.go is owned by
// a parallel agent (out of our file scope); wiring the command is that one line in run():
//
//	if cmd == "mcp" {
//		return mcpCommand(args)
//	}
//
// This file defines only the command itself.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/codeplo/codeplo/internal/app"
	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/modules/mcp"
)

func mcpCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: codeplo mcp check [--json]")
	}
	if args[0] != "check" {
		return fmt.Errorf("unknown mcp command %q (try: check)", args[0])
	}
	fs := flag.NewFlagSet("mcp check", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the state as one JSON object")
	if err := fs.Parse(args[1:]); err != nil {
		return err
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
	ctx := context.Background()

	path := mcp.SocketPath(cfg.DataDir)
	_, statErr := os.Stat(path)
	present := statErr == nil
	enabled := mcp.New(db).Enabled(ctx) // re-reads the modules row and the mcp.server feature flag
	tokenSet := os.Getenv(mcp.TokenEnv) != ""

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"socket": path, "socket_present": present, "enabled": enabled,
			"token_env": mcp.TokenEnv, "token_set": tokenSet,
		})
	}
	fmt.Printf("socket        %s\n", path)
	fmt.Printf("socket file   %s\n", mcpOnOff(present))
	fmt.Printf("module+flag   %s (modules.mcp + feature_flags.mcp.server)\n", mcpOnOff(enabled))
	fmt.Printf("token env     %s\n", mcp.TokenEnv)
	fmt.Printf("token bound   %s\n", mcpOnOff(tokenSet))
	// The token value is never printed, only whether one is bound.
	switch {
	case !enabled:
		fmt.Fprintln(os.Stderr, "mcp is off: enable the mcp module and the mcp.server feature flag")
	case !present:
		fmt.Fprintln(os.Stderr, "mcp is enabled but the socket is missing: is codeplod running?")
	case !tokenSet:
		fmt.Fprintln(os.Stderr, "no "+mcp.TokenEnv+" is set: connections are refused until a cpl_ API token is bound")
	}
	return nil
}

func mcpOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
