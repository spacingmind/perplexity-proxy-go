// Package mcp implements a stdio MCP server exposing the pplx client as
// tools. Protocol: newline-delimited JSON-RPC 2.0 on stdin/stdout; logs go
// to stderr only (stdout is the protocol channel).
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spacingmind/perplexity-proxy-go/internal/pplx"
	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

const protocolVersion = "2024-11-05"

// Server serves MCP over newline-delimited JSON-RPC 2.0. The client
// factory is the seam between production (token + utls transport, wired in
// cmd/pplx) and tests (fake client against httptest).
type Server struct {
	factory func() (transport.Client, *spec.Spec, error)
}

// New builds a Server from a transport-client factory.
func New(factory func() (transport.Client, *spec.Spec, error)) *Server {
	return &Server{factory: factory}
}

// Run reads messages from in until EOF, writing one response per request
// (notifications produce none) to out.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		resp := s.handleLine(ctx, line)
		if resp == nil {
			continue
		}
		if err := encoder.Encode(resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	return scanner.Err()
}

// rpcMessage is the union of request and notification shapes.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse is a JSON-RPC 2.0 response (result XOR error).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

var null = json.RawMessage("null")

var errInvalidParams = errors.New("invalid params")

func (s *Server) handleLine(ctx context.Context, line []byte) *rpcResponse {
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      null,
			Error:   &rpcError{Code: codeParseError, Message: "parse error"},
		}
	}
	// A request carries an id; notifications (no id) get no response.
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		return nil
	}
	result, rpcErr := s.dispatch(ctx, msg.Method, msg.Params)
	if rpcErr != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: msg.ID, Error: rpcErr}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: result}
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "pplx", "version": "0.1.0"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return toolsList(), nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call requires {name, arguments}"}
		}
		text, err := s.callTool(ctx, p.Name, p.Arguments)
		if err != nil {
			if errors.Is(err, errInvalidParams) {
				return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
			}
			return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
		}
		return map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
		}, nil
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("method %q not found", method)}
	}
}

func toolsList() any {
	return map[string]any{
		"tools": []map[string]any{
			{
				"name":        "pplx_ask",
				"description": "Ask Perplexity a question; returns the answer with numbered citations.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query":        map[string]any{"type": "string", "description": "The question to ask"},
						"model":        map[string]any{"type": "string", "description": "Model name or identifier (optional, e.g. best, glm, claude-sonnet-5)"},
						"source_focus": map[string]any{"type": "string", "description": "Source focus: web, scholar, social, edgar (optional)"},
					},
					"required": []string{"query"},
				},
			},
			{
				"name":        "pplx_deep_research",
				"description": "Run a Deep Research report (model deep-research). Slower; returns the full report.",
				"inputSchema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string", "description": "The research question"},
					},
					"required": []string{"query"},
				},
			},
			{
				"name":        "pplx_usage",
				"description": "Remaining Perplexity rate limits (Pro Search, Deep Research, Labs, Browser Agent, per-source caps).",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			{
				"name":        "pplx_spec_check",
				"description": "Smoke-test the Perplexity endpoints (rate-limits, ask, thread list) and report PASS/FAIL per endpoint. Consumes one query.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
	}
}

func (s *Server) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, sp, err := s.factory()
	if err != nil {
		return "", fmt.Errorf("client: %w", err)
	}

	switch name {
	case "pplx_ask":
		var a struct {
			Query       string `json:"query"`
			Model       string `json:"model"`
			SourceFocus string `json:"source_focus"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Query == "" {
			return "", fmt.Errorf("%w: pplx_ask requires {query}", errInvalidParams)
		}
		conv := pplx.NewConversation(t, sp)
		ans, err := conv.Ask(ctx, a.Query, pplx.AskOptions{Model: a.Model, SourceFocus: a.SourceFocus})
		if err != nil {
			return "", err
		}
		return formatAnswer(ans), nil

	case "pplx_deep_research":
		var a struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(args, &a); err != nil || a.Query == "" {
			return "", fmt.Errorf("%w: pplx_deep_research requires {query}", errInvalidParams)
		}
		conv := pplx.NewConversation(t, sp)
		ans, err := conv.Ask(ctx, a.Query, pplx.AskOptions{Model: "deep-research"})
		if err != nil {
			return "", err
		}
		return formatAnswer(ans), nil

	case "pplx_usage":
		rl, err := pplx.Usage(ctx, t, sp)
		if err != nil {
			return "", err
		}
		return formatUsage(rl), nil

	case "pplx_spec_check":
		return specCheck(ctx, t, sp), nil

	default:
		return "", fmt.Errorf("%w: unknown tool %q", errInvalidParams, name)
	}
}

// formatAnswer renders an Answer the way the CLI does: answer text then a
// numbered Sources list.
func formatAnswer(ans *pplx.Answer) string {
	if ans == nil || ans.Text == "" {
		return "(no answer returned)"
	}
	out := ans.Text
	if len(ans.Citations) > 0 {
		out += "\n\nSources:\n"
		for _, c := range ans.Citations {
			switch {
			case c.Title != "" && c.URL != "":
				out += fmt.Sprintf("[%d] %s — %s\n", c.Index, c.Title, c.URL)
			case c.URL != "":
				out += fmt.Sprintf("[%d] %s\n", c.Index, c.URL)
			default:
				out += fmt.Sprintf("[%d] %s\n", c.Index, c.Title)
			}
		}
	}
	return out
}

// formatUsage renders rate limits as plain text.
func formatUsage(rl *pplx.RateLimits) string {
	out := fmt.Sprintf("Pro Search: %d remaining\nDeep Research: %d remaining\nCreate Files & Apps: %d remaining\nBrowser Agent: %d remaining",
		rl.RemainingPro, rl.RemainingResearch, rl.RemainingLabs, rl.RemainingAgenticResearch)
	for _, s := range rl.SourceLimits {
		if s.Unlimited() || s.Remaining == nil || s.MonthlyLimit == nil {
			continue
		}
		out += fmt.Sprintf("\nsource %s: %d/%d", s.SourceID, *s.Remaining, *s.MonthlyLimit)
	}
	return out
}

// specCheck smoke-tests rate-limits -> ask -> thread-list (mirrors the
// CLI's spec check --live) and returns a PASS/FAIL report.
func specCheck(ctx context.Context, t transport.Client, sp *spec.Spec) string {
	type step struct {
		name string
		err  error
	}
	var steps []step

	if _, err := pplx.Usage(ctx, t, sp); err != nil {
		steps = append(steps, step{"rate-limits", err})
	} else {
		steps = append(steps, step{"rate-limits", nil})
	}

	conv := pplx.NewConversation(t, sp)
	if _, err := conv.Ask(ctx, "ping", pplx.AskOptions{}); err != nil {
		steps = append(steps, step{"ask", err})
	} else {
		steps = append(steps, step{"ask", nil})
	}

	var threadList any
	if err := t.PostJSON(ctx, sp.Endpoints.ListThreads, map[string]any{"limit": 1, "offset": 0, "search_term": ""}, &threadList); err != nil {
		steps = append(steps, step{"thread-list", err})
	} else {
		steps = append(steps, step{"thread-list", nil})
	}

	out := ""
	failed := 0
	for _, st := range steps {
		if st.err == nil {
			out += fmt.Sprintf("PASS %s\n", st.name)
		} else {
			failed++
			out += fmt.Sprintf("FAIL %s: %v\n", st.name, st.err)
		}
	}
	if failed == 0 {
		out += "All endpoints OK."
	}
	return out
}
