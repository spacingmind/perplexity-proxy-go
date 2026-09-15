package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// fakePplx is an httptest-backed Perplexity serving the shapes the tools
// need: ask SSE, rate limits, thread list.
func fakePplx(t *testing.T) (*spec.Spec, func() (transport.Client, *spec.Spec, error)) {
	t.Helper()
	const sse = "data: {\"backend_uuid\":\"u1\",\"thread_title\":\"T\"}\n" +
		"data: {\"text\":\"{\\\"answer\\\":\\\"Paris [1] is the capital.\\\",\\\"web_results\\\":[{\\\"name\\\":\\\"Wiki\\\",\\\"url\\\":\\\"https://w.example\\\"},{\\\"name\\\":\\\"Brit\\\",\\\"url\\\":\\\"https://b.example\\\"}]}\"}\n" +
		"data: {\"final\":true}\n"

	var lastAskBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/search/new", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/rest/sse/perplexity_ask", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&lastAskBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
		w.(http.Flusher).Flush()
	})
	mux.HandleFunc("/rest/rate-limit/all", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"remaining_pro":4,"remaining_research":2,"remaining_labs":9,"remaining_agentic_research":1,
			"sources":{"source_to_limit":{"web":{"monthly_limit":null},"scholar":{"monthly_limit":50,"remaining":12}}}}`)
	})
	mux.HandleFunc("/rest/thread/list_ask_threads", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	sp := &spec.Spec{BaseURL: srv.URL, APIVersion: "2.18"}
	sp.Endpoints.SearchInit = "/search/new"
	sp.Endpoints.Ask = "/rest/sse/perplexity_ask"
	sp.Endpoints.RateLimits = "/rest/rate-limit/all"
	sp.Endpoints.ListThreads = "/rest/thread/list_ask_threads"
	sp.Defaults = spec.Defaults{Language: "en-US", SourceFocus: "web", SearchFocus: "internet"}
	sp.Models = []spec.Model{
		{Name: "best", Identifier: "pplx_pro", Mode: "copilot"},
		{Name: "deep-research", Identifier: "pplx_alpha", Mode: "copilot"},
	}

	factory := func() (transport.Client, *spec.Spec, error) {
		c, err := transport.NewPlain(transport.Options{BaseURL: srv.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
		if err != nil {
			return nil, nil, err
		}
		return c, sp, nil
	}
	t.Cleanup(func() {})
	return sp, factory
}

// session drives a Server over an in-memory pipe: send lines, collect the
// parsed responses in order.
type session struct {
	srv  *Server
	in   bytes.Buffer
	out  bytes.Buffer
	resp []json.RawMessage
}

func newSession(t *testing.T, factory func() (transport.Client, *spec.Spec, error)) *session {
	s := &session{srv: New(factory)}
	if err := s.srv.Run(context.Background(), &s.in, &s.out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(s.out.Bytes()))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		s.resp = append(s.resp, raw)
	}
	return s
}

func (s *session) send(t *testing.T, line string) {
	s.in.Reset()
	s.out.Reset()
	s.in.WriteString(line + "\n")
	s.resp = nil
	if err := s.srv.Run(context.Background(), &s.in, &s.out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(s.out.Bytes()))
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		s.resp = append(s.resp, raw)
	}
}

func (s *session) last(t *testing.T) map[string]any {
	t.Helper()
	if len(s.resp) == 0 {
		t.Fatal("no response produced")
	}
	var m map[string]any
	if err := json.Unmarshal(s.resp[len(s.resp)-1], &m); err != nil {
		t.Fatalf("response not a JSON object: %v (%s)", err, s.resp[len(s.resp)-1])
	}
	return m
}

func TestMCP_InitializeHandshake(t *testing.T) {
	_, factory := fakePplx(t)
	s := newSession(t, factory)

	s.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	init := s.last(t)
	if init["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v", init["jsonrpc"])
	}
	if init["id"] != float64(1) {
		t.Errorf("id echoed wrong: %v", init["id"])
	}
	res := init["result"].(map[string]any)
	if res["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v", res["protocolVersion"])
	}
	caps := res["capabilities"].(map[string]any)
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities missing tools: %v", caps)
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != "pplx" {
		t.Errorf("serverInfo.name = %v", info["name"])
	}

	s.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := s.last(t)["result"].(map[string]any)["tools"].([]any)
	if len(list) != 4 {
		t.Fatalf("tools = %d, want 4", len(list))
	}
	names := map[string]bool{}
	for _, tl := range list {
		tool := tl.(map[string]any)
		names[tool["name"].(string)] = true
		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Errorf("%s inputSchema.type = %v", tool["name"], schema["type"])
		}
		if _, ok := schema["properties"]; !ok {
			t.Errorf("%s inputSchema missing properties", tool["name"])
		}
	}
	for _, want := range []string{"pplx_ask", "pplx_deep_research", "pplx_usage", "pplx_spec_check"} {
		if !names[want] {
			t.Errorf("missing tool %s", want)
		}
	}
	// pplx_ask declares query required
	for _, tl := range list {
		tool := tl.(map[string]any)
		if tool["name"] == "pplx_ask" {
			req := tool["inputSchema"].(map[string]any)["required"].([]any)
			if len(req) != 1 || req[0] != "query" {
				t.Errorf("pplx_ask required = %v", req)
			}
		}
	}
}

func TestMCP_ToolsCallAsk(t *testing.T) {
	_, factory := fakePplx(t)
	s := newSession(t, factory)

	s.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pplx_ask","arguments":{"query":"capital of france?"}}}`)
	resp := s.last(t)
	res := resp["result"].(map[string]any)
	content := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", content)
	}
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Paris [1] is the capital.") {
		t.Errorf("answer missing: %q", text)
	}
	if !strings.Contains(text, "Sources:") || !strings.Contains(text, "[1] Wiki — https://w.example") {
		t.Errorf("citations missing: %q", text)
	}
}

func TestMCP_ToolsCallDeepResearch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/new" {
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"text\":\"{\\\"answer\\\":\\\"report\\\"}\"}\ndata: {\"final\":true}\n")
	}))
	defer srv.Close()
	sp := &spec.Spec{BaseURL: srv.URL, APIVersion: "2.18"}
	sp.Endpoints.SearchInit = "/search/new"
	sp.Endpoints.Ask = "/rest/sse/perplexity_ask"
	sp.Models = []spec.Model{{Name: "deep-research", Identifier: "pplx_alpha", Mode: "copilot"}}
	s := newSession(t, func() (transport.Client, *spec.Spec, error) {
		c, err := transport.NewPlain(transport.Options{BaseURL: srv.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
		return c, sp, err
	})
	s.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pplx_deep_research","arguments":{"query":"topic"}}}`)
	text := s.last(t)["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != "report" {
		t.Errorf("text = %q", text)
	}
}

func TestMCP_ToolsCallUsage(t *testing.T) {
	_, factory := fakePplx(t)
	s := newSession(t, factory)

	s.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pplx_usage","arguments":{}}}`)
	text := s.last(t)["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{"Pro Search: 4", "Deep Research: 2", "Create Files & Apps: 9", "Browser Agent: 1", "source scholar: 12/50"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestMCP_ToolsCallSpecCheck(t *testing.T) {
	_, factory := fakePplx(t)
	s := newSession(t, factory)

	s.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pplx_spec_check","arguments":{}}}`)
	text := s.last(t)["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{"PASS rate-limits", "PASS ask", "PASS thread-list", "All endpoints OK."} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
}

func TestMCP_UnknownMethodError(t *testing.T) {
	_, factory := fakePplx(t)
	s := newSession(t, factory)

	s.send(t, `{"jsonrpc":"2.0","id":7,"method":"resources/list"}`)
	resp := s.last(t)
	if resp["error"] == nil {
		t.Fatalf("expected error response, got %v", resp)
	}
	e := resp["error"].(map[string]any)
	if e["code"] != float64(-32601) {
		t.Errorf("code = %v, want -32601", e["code"])
	}
	if id := resp["id"]; id != float64(7) {
		t.Errorf("id = %v", id)
	}
}

func TestMCP_BadParamsError(t *testing.T) {
	_, factory := fakePplx(t)
	s := newSession(t, factory)

	// missing name
	s.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`)
	e := s.last(t)["error"].(map[string]any)
	if e["code"] != float64(-32602) {
		t.Errorf("code = %v, want -32602", e["code"])
	}
	// ask without query
	s.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"pplx_ask","arguments":{}}}`)
	e = s.last(t)["error"].(map[string]any)
	if e["code"] != float64(-32602) {
		t.Errorf("code = %v, want -32602", e["code"])
	}
	// unknown tool
	s.send(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	e = s.last(t)["error"].(map[string]any)
	if e["code"] != float64(-32602) {
		t.Errorf("code = %v, want -32602", e["code"])
	}
}

func TestMCP_NotificationNoResponse(t *testing.T) {
	_, factory := fakePplx(t)
	s := newSession(t, factory)

	// initialized notification has no id -> no response line at all
	s.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(s.resp) != 0 {
		t.Errorf("notification produced %d responses, want 0", len(s.resp))
	}
	// cancelled notification likewise
	s.send(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	if len(s.resp) != 0 {
		t.Errorf("cancelled produced %d responses, want 0", len(s.resp))
	}
}

func TestMCP_MultipleLinesOneRun(t *testing.T) {
	_, factory := fakePplx(t)
	in := bytes.NewBufferString(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
	var out bytes.Buffer
	if err := New(factory).Run(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&out)
	var ids []float64
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			break
		}
		ids = append(ids, m["id"].(float64))
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("ids = %v, want [1 2] (notification skipped)", ids)
	}
}

func TestMCP_ParseError(t *testing.T) {
	_, factory := fakePplx(t)
	s := newSession(t, factory)
	s.send(t, `{not json`)
	resp := s.last(t)
	if resp["error"] == nil || resp["error"].(map[string]any)["code"] != float64(-32700) {
		t.Fatalf("expected -32700, got %v", resp)
	}
}
