package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// fakePplx serves the ask SSE + captures request payloads so tests can
// assert followups and model mapping on the wire.
type fakePplx struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

const askSSE = "data: {\"backend_uuid\":\"uuid-a\",\"thread_title\":\"T\"}\n" +
	"data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"progress\":\"IN_PROGRESS\",\"chunks\":[\"Hel\",\"lo\"]}}]}\n" +
	"data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"progress\":\"DONE\",\"answer\":\"Hello\",\"chunks\":[\"Hel\",\"lo\"]}}]}\n" +
	"data: {\"final\":true}\n"

func newFakePplx(t *testing.T) *fakePplx {
	t.Helper()
	f := &fakePplx{}
	mux := http.NewServeMux()
	mux.HandleFunc("/search/new", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/rest/sse/perplexity_ask", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, askSSE)
		w.(http.Flusher).Flush()
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePplx) payloads() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.bodies))
	copy(out, f.bodies)
	return out
}

func (f *fakePplx) spec() *spec.Spec {
	sp := &spec.Spec{BaseURL: f.srv.URL, APIVersion: "2.18"}
	sp.Endpoints.SearchInit = "/search/new"
	sp.Endpoints.Ask = "/rest/sse/perplexity_ask"
	sp.Defaults = spec.Defaults{Language: "en-US", SourceFocus: "web", SearchFocus: "internet"}
	sp.Models = []spec.Model{
		{Name: "best", Identifier: "pplx_pro", Mode: "copilot"},
		{Name: "claude-sonnet-5", Identifier: "claude50sonnet", Mode: "copilot"},
		{Name: "claude-opus-4.8", Identifier: "claude48opus", Mode: "copilot"},
		{Name: "gpt-5.6", Identifier: "gpt56_terra", Mode: "copilot"},
	}
	return sp
}

func (f *fakePplx) factory() ClientFactory {
	sp := f.spec()
	return func() (transport.Client, *spec.Spec, error) {
		c, err := transport.NewPlain(transport.Options{BaseURL: f.srv.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
		return c, sp, err
	}
}

func newTestServer(t *testing.T, apiKey string) (*fakePplx, *httptest.Server) {
	t.Helper()
	f := newFakePplx(t)
	ts := httptest.NewServer(New(f.factory(), apiKey).Handler())
	t.Cleanup(ts.Close)
	return f, ts
}

func post(t *testing.T, ts *httptest.Server, headers map[string]string, body any) (*http.Response, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ts.URL+"/v1/messages", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp, buf.String()
}

func TestAPI_Messages_SingleTurn(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, body := post(t, ts, nil, map[string]any{
		"model":      "claude-sonnet-5",
		"max_tokens": 1024,
		"messages":   []map[string]any{{"role": "user", "content": "hi there"}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var m message
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, body)
	}
	if m.Type != "message" || m.Role != "assistant" {
		t.Errorf("type/role = %s/%s", m.Type, m.Role)
	}
	if m.ID != "msg_uuid-a" {
		t.Errorf("id = %s", m.ID)
	}
	if m.Model != "claude-sonnet-5" {
		t.Errorf("model echoed = %s", m.Model)
	}
	if len(m.Content) != 1 || m.Content[0].Type != "text" || m.Content[0].Text != "Hello" {
		t.Errorf("content = %+v", m.Content)
	}
	if m.StopReason != "end_turn" || m.StopSequence != nil {
		t.Errorf("stop = %s/%v", m.StopReason, m.StopSequence)
	}
	if m.Usage.InputTokens == 0 || m.Usage.OutputTokens == 0 {
		t.Errorf("usage = %+v", m.Usage)
	}
}

func TestAPI_Messages_Stream(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, body := post(t, ts, nil, map[string]any{
		"model":    "best",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %s", ct)
	}

	// Parse event/data pairs.
	type ev struct{ event, data string }
	var events []ev
	sc := bufio.NewScanner(strings.NewReader(body))
	var cur string
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			cur = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			events = append(events, ev{cur, strings.TrimPrefix(line, "data: ")})
		}
	}
	var names []string
	for _, e := range events {
		names = append(names, e.event)
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if len(names) != len(want) {
		t.Fatalf("events = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("events = %v, want %v", names, want)
		}
	}
	if events[len(events)-1].event != "message_stop" {
		t.Errorf("last event = %s", events[len(events)-1].event)
	}
	// deltas carry the streamed chunks in order
	var deltaTexts []string
	for _, e := range events {
		if e.event != "content_block_delta" {
			continue
		}
		var d struct {
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
		}
		json.Unmarshal([]byte(e.data), &d)
		deltaTexts = append(deltaTexts, d.Delta.Text)
	}
	if strings.Join(deltaTexts, "") != "Hello" {
		t.Errorf("joined deltas = %q, want Hello", strings.Join(deltaTexts, ""))
	}
	// message_start has the Anthropic message shape
	var ms struct {
		Message struct {
			Role string `json:"role"`
			Type string `json:"type"`
		} `json:"message"`
	}
	json.Unmarshal([]byte(events[0].data), &ms)
	if ms.Message.Role != "assistant" || ms.Message.Type != "message" {
		t.Errorf("message_start.message = %+v", ms.Message)
	}
}

func TestAPI_Messages_MultiTurnFollowup(t *testing.T) {
	f, ts := newTestServer(t, "")
	for _, q := range []string{"first question", "second question"} {
		_, body := post(t, ts, nil, map[string]any{
			"model":    "best",
			"messages": []map[string]any{{"role": "user", "content": q}},
		})
		if !strings.Contains(body, "Hello") {
			t.Fatalf("unexpected body: %s", body)
		}
	}
	payloads := f.payloads()
	if len(payloads) != 2 {
		t.Fatalf("payloads = %d", len(payloads))
	}
	if _, has := payloads[0]["params"].(map[string]any)["last_backend_uuid"]; has {
		t.Error("first request must not send last_backend_uuid")
	}
	params := payloads[1]["params"].(map[string]any)
	if got := params["last_backend_uuid"]; got != "uuid-a" {
		t.Errorf("second request last_backend_uuid = %v, want uuid-a", got)
	}
	if got := params["query_source"]; got != "followup" {
		t.Errorf("query_source = %v", got)
	}
}

func TestAPI_AuthRequired(t *testing.T) {
	_, ts := newTestServer(t, "sekrit")

	resp, body := post(t, ts, nil, map[string]any{
		"model": "best", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != 401 {
		t.Fatalf("no-key status = %d", resp.StatusCode)
	}
	var eb map[string]any
	json.Unmarshal([]byte(body), &eb)
	if eb["type"] != "error" {
		t.Errorf("body = %s", body)
	}
	if inner := eb["error"].(map[string]any); inner["type"] != "authentication_error" {
		t.Errorf("error.type = %v", inner["type"])
	}

	// x-api-key header works
	resp, _ = post(t, ts, map[string]string{"x-api-key": "sekrit"}, map[string]any{
		"model": "best", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != 200 {
		t.Errorf("x-api-key status = %d", resp.StatusCode)
	}
	// Authorization: Bearer works
	f2, ts2 := newTestServer(t, "sekrit")
	_ = f2
	resp, _ = post(t, ts2, map[string]string{"Authorization": "Bearer sekrit"}, map[string]any{
		"model": "best", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != 200 {
		t.Errorf("bearer status = %d", resp.StatusCode)
	}
	// wrong key rejected
	resp, _ = post(t, ts, map[string]string{"x-api-key": "wrong"}, map[string]any{
		"model": "best", "messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != 401 {
		t.Errorf("wrong-key status = %d", resp.StatusCode)
	}
}

func TestAPI_ModelMapping(t *testing.T) {
	f, ts := newTestServer(t, "")
	_, body := post(t, ts, nil, map[string]any{
		"model":    "claude-sonnet-5",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if !strings.Contains(body, "Hello") {
		t.Fatalf("body = %s", body)
	}
	params := f.payloads()[0]["params"].(map[string]any)
	// The spec lookup resolves the anthropic-style name to the identifier.
	if got := params["model_preference"]; got != "claude50sonnet" {
		t.Errorf("model_preference = %v, want claude50sonnet", got)
	}
	// native identifier passes through
	_, _ = post(t, ts, nil, map[string]any{
		"model":    "pplx_pro",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	params = f.payloads()[1]["params"].(map[string]any)
	if got := params["model_preference"]; got != "pplx_pro" {
		t.Errorf("native pass-through model_preference = %v", got)
	}
}

func TestAPI_SystemPrompt(t *testing.T) {
	f, ts := newTestServer(t, "")
	_, body := post(t, ts, nil, map[string]any{
		"model":    "best",
		"system":   "You are terse.",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if !strings.Contains(body, "Hello") {
		t.Fatalf("body = %s", body)
	}
	q := f.payloads()[0]["query_str"].(string)
	if !strings.HasPrefix(q, "system: You are terse.\n") {
		t.Errorf("query_str = %q, want system prefix", q)
	}
	if !strings.Contains(q, "user: hi") {
		t.Errorf("query_str = %q, want user turn", q)
	}
}

func TestAPI_MultiTurnFlattening(t *testing.T) {
	f, ts := newTestServer(t, "")
	_, _ = post(t, ts, nil, map[string]any{
		"model": "best",
		"messages": []map[string]any{
			{"role": "user", "content": "first"},
			{"role": "assistant", "content": "answer"},
			{"role": "user", "content": "followup"},
		},
	})
	q := f.payloads()[0]["query_str"].(string)
	want := "user: first\nassistant: answer\nuser: followup"
	if q != want {
		t.Errorf("query_str = %q, want %q", q, want)
	}
}

func TestAPI_UnsupportedContentBlock(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, body := post(t, ts, nil, map[string]any{
		"model": "best",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "look"},
				{"type": "image", "source": "..."},
			},
		}},
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "image") {
		t.Errorf("error should name the block type: %s", body)
	}
}

func TestAPI_ContentBlockList(t *testing.T) {
	f, ts := newTestServer(t, "")
	_, _ = post(t, ts, nil, map[string]any{
		"model": "best",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "part one"},
				{"type": "text", "text": "part two"},
			},
		}},
	})
	q := f.payloads()[0]["query_str"].(string)
	if !strings.Contains(q, "part one\npart two") {
		t.Errorf("query_str = %q", q)
	}
}

func TestAPI_BadRole(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, _ := post(t, ts, nil, map[string]any{
		"model":    "best",
		"messages": []map[string]any{{"role": "system", "content": "hi"}},
	})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestAPI_MissingMessages(t *testing.T) {
	_, ts := newTestServer(t, "")
	resp, _ := post(t, ts, nil, map[string]any{"model": "best"})
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	resp, _ = post(t, ts, nil, map[string]any{
		"model": "best", "messages": []map[string]any{},
	})
	if resp.StatusCode != 400 {
		t.Fatalf("empty messages status = %d", resp.StatusCode)
	}
}

func TestAPI_ConversationHeaderIsolation(t *testing.T) {
	f, ts := newTestServer(t, "")
	post(t, ts, map[string]string{"x-pplx-conversation": "a"}, map[string]any{
		"model": "best", "messages": []map[string]any{{"role": "user", "content": "one"}},
	})
	// different conversation id -> no followup
	post(t, ts, map[string]string{"x-pplx-conversation": "b"}, map[string]any{
		"model": "best", "messages": []map[string]any{{"role": "user", "content": "two"}},
	})
	payloads := f.payloads()
	if _, has := payloads[1]["params"].(map[string]any)["last_backend_uuid"]; has {
		t.Error("different conversation id must not be a followup")
	}
	// same conversation id -> followup
	post(t, ts, map[string]string{"x-pplx-conversation": "a"}, map[string]any{
		"model": "best", "messages": []map[string]any{{"role": "user", "content": "three"}},
	})
	payloads = f.payloads()
	if got := payloads[2]["params"].(map[string]any)["last_backend_uuid"]; got != "uuid-a" {
		t.Errorf("same conversation id should follow up, got %v", got)
	}
}
