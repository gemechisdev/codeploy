// Package mcp implements a flag-gated MCP server for AI agents (P7/J7).
// No OpenAPI routes (T53): tools run in-process with the same permission checks as the API.
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Server is a minimal MCP tool host. Transport is newline-delimited JSON on in/out (stdio or unix).
type Server struct {
	DB *sql.DB
	// CheckPerm returns nil if the bound principal may use perm.
	CheckPerm func(perm string) error
	// ListProjects / ListResources / TriggerDeploy are injected by the daemon so this package does not import api.
	ListProjects  func(ctx context.Context) ([]map[string]string, error)
	ListResources func(ctx context.Context) ([]map[string]string, error)
	TriggerDeploy func(ctx context.Context, resourceID string) (deploymentID string, err error)
}

func New(db *sql.DB) *Server { return &Server{DB: db} }

func (s *Server) Enabled(ctx context.Context) bool {
	var e int
	if err := s.DB.QueryRowContext(ctx, `SELECT enabled FROM modules WHERE key='mcp'`).Scan(&e); err != nil {
		return false
	}
	if e != 1 {
		return false
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT enabled FROM feature_flags WHERE key='mcp.server'`).Scan(&e); err != nil {
		return false
	}
	return e == 1
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcRes struct {
	JSONRPC string  `json:"jsonrpc"`
	ID      any     `json:"id"`
	Result  any     `json:"result,omitempty"`
	Error   *rpcErr `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads NDJSON requests from r and writes responses to w until EOF.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	if !s.Enabled(ctx) {
		return ErrDisabled
	}
	dec := json.NewDecoder(r)
	enc := json.NewEncoder(w)
	for {
		var req rpcReq
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		// Re-read the module/flag gate on every request. Disabling the module mid-connection then takes effect
		// without a daemon restart, and the refusal is a real server-side message rather than a vanished endpoint.
		if !s.Enabled(ctx) {
			_ = enc.Encode(rpcRes{JSONRPC: "2.0", ID: req.ID, Error: &rpcErr{Code: -32000, Message: ErrDisabled.Error()}})
			continue
		}
		res := s.dispatch(ctx, req)
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req rpcReq) rpcRes {
	out := rpcRes{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		out.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]string{"name": "codeplo", "version": "0.1.0"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}
	case "tools/list":
		out.Result = map[string]any{"tools": s.toolDefs()}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			out.Error = &rpcErr{Code: -32602, Message: err.Error()}
			return out
		}
		text, err := s.callTool(ctx, p.Name, p.Arguments)
		if err != nil {
			out.Error = &rpcErr{Code: -32000, Message: err.Error()}
			return out
		}
		out.Result = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
	default:
		out.Error = &rpcErr{Code: -32601, Message: "method not found"}
	}
	return out
}

func (s *Server) toolDefs() []map[string]any {
	return []map[string]any{
		{"name": "list_projects", "description": "List projects (requires resource:read)", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "list_resources", "description": "List resources (requires resource:read)", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "trigger_deploy", "description": "Enqueue a deploy for a resource id (requires deploy:create)", "inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource_id": map[string]string{"type": "string"},
			},
			"required": []string{"resource_id"},
		}},
	}
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	switch name {
	case "list_projects":
		if err := s.perm("resource:read"); err != nil {
			return "", err
		}
		if s.ListProjects == nil {
			return "", fmt.Errorf("list_projects is not available: its store is not wired")
		}
		items, err := s.ListProjects(ctx)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(items)
		return string(b), nil
	case "list_resources":
		if err := s.perm("resource:read"); err != nil {
			return "", err
		}
		if s.ListResources == nil {
			return "", fmt.Errorf("list_resources is not available: its store is not wired")
		}
		items, err := s.ListResources(ctx)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(items)
		return string(b), nil
	case "trigger_deploy":
		if err := s.perm("deploy:create"); err != nil {
			return "", err
		}
		rid, _ := args["resource_id"].(string)
		rid = strings.TrimSpace(rid)
		if rid == "" {
			return "", fmt.Errorf("resource_id required")
		}
		if s.TriggerDeploy == nil {
			return "", fmt.Errorf("deploy is not available")
		}
		id, err := s.TriggerDeploy(ctx, rid)
		if err != nil {
			return "", err
		}
		return id, nil
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func (s *Server) perm(p string) error {
	if s.CheckPerm == nil {
		return fmt.Errorf("mcp is not bound to a principal")
	}
	return s.CheckPerm(p)
}
