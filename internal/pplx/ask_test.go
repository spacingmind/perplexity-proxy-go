package pplx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

const sseFixture = "data: {\"backend_uuid\":\"uuid-1\",\"thread_title\":\"Q1\"}\n" +
	"\n" +
	"data: {\"text\":\"{\\\"chunks\\\":[\\\"Hel\\\",\\\"lo \\\",\\\"world\\\"],\\\"web_results\\\":[{\\\"name\\\":\\\"First\\\",\\\"url\\\":\\\"https://a.example\\\"},{\\\"name\\\":\\\"Second\\\",\\\"url\\\":\\\"https://b.example\\\"}]}\",\"unknown_field\":{\"weird\":[1,2,3]}}\n" +
	"\n" +
	"data: {\"final\":true}\n"

type askServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newAskServer(t *testing.T, sse string) *askServer {
	t.Helper()
	ts := &askServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/search/new", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			http.Error(w, "missing q", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/rest/sse/perplexity_ask", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		ts.mu.Lock()
		ts.bodies = append(ts.bodies, body)
		ts.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		flush := w.(http.Flusher)
		fmt.Fprint(w, sse)
		flush.Flush()
	})
	ts.srv = httptest.NewServer(mux)
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *askServer) askBodies() []map[string]any {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := make([]map[string]any, len(ts.bodies))
	copy(out, ts.bodies)
	return out
}

func askTestSetup(t *testing.T, sse string) (*askServer, *Conversation) {
	t.Helper()
	ts := newAskServer(t, sse)
	c, err := transport.NewPlain(transport.Options{BaseURL: ts.srv.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sp := spec.Spec{BaseURL: ts.srv.URL, APIVersion: "2.18"}
	sp.Endpoints.SearchInit = "/search/new"
	sp.Endpoints.Ask = "/rest/sse/perplexity_ask"
	sp.Defaults = spec.Defaults{
		PromptSource: "user", SendBackTextInStreaming: true,
		Language: "en-US", SourceFocus: "web", SearchFocus: "internet",
	}
	sp.Models = []spec.Model{
		{Name: "best", Identifier: "pplx_pro", Mode: "copilot"},
		{Name: "glm", Identifier: "glm_5_2", Mode: "copilot"},
	}
	return ts, NewConversation(c, &sp)
}

func TestAsk_StreamsAnswer(t *testing.T) {
	_, conv := askTestSetup(t, sseFixture)
	ans, err := conv.Ask(context.Background(), "hello?", AskOptions{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Text != "Hello world" {
		t.Errorf("Text = %q, want chunk-joined 'Hello world'", ans.Text)
	}
	if len(ans.Citations) != 2 {
		t.Fatalf("citations = %+v", ans.Citations)
	}
	if ans.Citations[0].Index != 1 || ans.Citations[0].URL != "https://a.example" || ans.Citations[0].Title != "First" {
		t.Errorf("citation[0] = %+v", ans.Citations[0])
	}
	if ans.Citations[1].Index != 2 || ans.Citations[1].URL != "https://b.example" {
		t.Errorf("citation[1] = %+v", ans.Citations[1])
	}
	if ans.ThreadTitle != "Q1" || ans.UUID != "uuid-1" {
		t.Errorf("title=%q uuid=%q", ans.ThreadTitle, ans.UUID)
	}
}

func TestAsk_AnswerFieldShape(t *testing.T) {
	// FINAL step shape: {"text":"[{\"step_type\":\"FINAL\",\"content\":{\"answer\":\"...\"}}]"}
	sse := "data: {\"backend_uuid\":\"u\",\"text\":\"[{\\\"step_type\\\":\\\"FINAL\\\",\\\"content\\\":{\\\"answer\\\":\\\"full answer\\\",\\\"web_results\\\":[{\\\"name\\\":\\\"N\\\",\\\"url\\\":\\\"https://x\\\"}]}}]\"}\n"
	_, conv := askTestSetup(t, sse)
	ans, err := conv.Ask(context.Background(), "q", AskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "full answer" {
		t.Errorf("Text = %q", ans.Text)
	}
	if len(ans.Citations) != 1 || ans.Citations[0].URL != "https://x" {
		t.Errorf("citations = %+v", ans.Citations)
	}
}

func TestAsk_IgnoresUnknownFields(t *testing.T) {
	// Fixture laced with unknown events, wrong-typed fields, non-JSON
	// frames, and a "blocks" event mixing known and unknown blocks.
	sse := "event: ping\n" +
		": keepalive comment\n" +
		"data: not-json\n" +
		"data: [1,2,3]\n" +
		"data: {\"totally\":\"unknown\",\"nested\":{\"deep\":[{\"x\":1}]}}\n" +
		"data: {\"backend_uuid\":12345,\"final\":\"yes\"}\n" + // wrong types
		"data: {\"blocks\":[{\"intended_usage\":\"unknown_usage\",\"payload\":true},{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"answer\":\"blocks answer\",\"chunks\":[\"ignored \",\"chunks\"]}}]}\n" +
		"data: {\"text\":\"{\\\"answer\\\":\\\"final answer\\\",\\\"new_protocol_field\\\":{\\\"a\\\":[1,2]}}\",\"surprise_field\":null}\n" +
		"data: {\"final\":true}\n"
	_, conv := askTestSetup(t, sse)
	ans, err := conv.Ask(context.Background(), "q", AskOptions{})
	if err != nil {
		t.Fatalf("Ask with unknown fields: %v", err)
	}
	if ans.Text != "final answer" {
		t.Errorf("Text = %q, want 'final answer' (later answer must win)", ans.Text)
	}
}

func TestFollowup_SendsBackendUUID(t *testing.T) {
	ts, conv := askTestSetup(t, sseFixture)
	ctx := context.Background()

	if _, err := conv.Ask(ctx, "first question", AskOptions{}); err != nil {
		t.Fatalf("first Ask: %v", err)
	}
	bodies := ts.askBodies()
	if len(bodies) != 1 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	if _, has := bodies[0]["params"].(map[string]any)["last_backend_uuid"]; has {
		t.Error("first ask must not send last_backend_uuid")
	}
	if bodies[0]["query_str"] != "first question" {
		t.Errorf("query_str = %v", bodies[0]["query_str"])
	}

	if _, err := conv.Ask(ctx, "second question", AskOptions{}); err != nil {
		t.Fatalf("second Ask: %v", err)
	}
	bodies = ts.askBodies()
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	params := bodies[1]["params"].(map[string]any)
	if got := params["last_backend_uuid"]; got != "uuid-1" {
		t.Errorf("last_backend_uuid = %v, want uuid-1", got)
	}
	if got := params["query_source"]; got != "followup" {
		t.Errorf("query_source = %v, want followup", got)
	}
	if _, has := params["read_write_token"]; has {
		t.Error("read_write_token must be absent when stream sent none")
	}
}

func TestAsk_PayloadShape(t *testing.T) {
	ts, conv := askTestSetup(t, sseFixture)
	if _, err := conv.Ask(context.Background(), "q", AskOptions{Model: "glm", SourceFocus: "scholar"}); err != nil {
		t.Fatal(err)
	}
	params := ts.askBodies()[0]["params"].(map[string]any)
	want := map[string]any{
		"model_preference":                "glm_5_2",
		"mode":                            "copilot",
		"sources":                         []any{"scholar"},
		"search_focus":                    "internet",
		"language":                        "en-US",
		"prompt_source":                   "user",
		"send_back_text_in_streaming_api": true,
		"use_schematized_api":             false,
		"is_incognito":                    true,
		"version":                         "2.18",
	}
	for k, v := range want {
		if !reflect.DeepEqual(params[k], v) {
			t.Errorf("params[%s] = %v (%T), want %v (%T)", k, params[k], params[k], v, v)
		}
	}
}

func TestAsk_RateLimited(t *testing.T) {
	sse := "data: {\"error_code\":\"FREE_TIER_RATE_LIMITED\"}\n"
	_, conv := askTestSetup(t, sse)
	_, err := conv.Ask(context.Background(), "q", AskOptions{})
	if err == nil || !strings.Contains(err.Error(), ErrRateLimited.Error()) {
		t.Fatalf("err = %v, want rate limit error", err)
	}
}

func TestUsage_ParsesRateLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/rate-limit/all" {
			http.Error(w, "nf", http.StatusNotFound)
			return
		}
		io.WriteString(w, `{
			"remaining_pro": 4,
			"remaining_research": 2,
			"remaining_labs": 9,
			"remaining_agentic_research": 0,
			"model_specific_limits": {"gpt56_terra": {"remaining": 3}},
			"sources": {"source_to_limit": {
				"web": {"monthly_limit": null, "remaining": null},
				"scholar": {"monthly_limit": 50, "remaining": 12},
				"brand_new_source": {"monthly_limit": 5, "remaining": 1, "extra": "ignored"}
			}},
			"future_field": {"anything": true}
		}`)
	}))
	defer srv.Close()

	c, err := transport.NewPlain(transport.Options{BaseURL: srv.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sp := spec.Spec{BaseURL: srv.URL}
	sp.Endpoints.RateLimits = "/rest/rate-limit/all"

	rl, err := Usage(context.Background(), c, &sp)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if rl.RemainingPro != 4 || rl.RemainingResearch != 2 || rl.RemainingLabs != 9 || rl.RemainingAgenticResearch != 0 {
		t.Errorf("buckets = %+v", rl)
	}
	if len(rl.SourceLimits) != 3 {
		t.Fatalf("source limits = %+v", rl.SourceLimits)
	}
	byID := map[string]SourceLimit{}
	for _, s := range rl.SourceLimits {
		byID[s.SourceID] = s
	}
	if !byID["web"].Unlimited() {
		t.Errorf("web should be unlimited: %+v", byID["web"])
	}
	if byID["scholar"].Remaining == nil || *byID["scholar"].Remaining != 12 {
		t.Errorf("scholar remaining = %v", byID["scholar"].Remaining)
	}
	if byID["scholar"].MonthlyLimit == nil || *byID["scholar"].MonthlyLimit != 50 {
		t.Errorf("scholar monthly = %v", byID["scholar"].MonthlyLimit)
	}
	if len(rl.ModelSpecificLimits) != 1 {
		t.Errorf("model limits = %v", rl.ModelSpecificLimits)
	}
}

func TestAsk_ClarifyingQuestions(t *testing.T) {
	steps := []any{map[string]any{
		"step_type": "RESEARCH_CLARIFYING_QUESTIONS",
		"content":   map[string]any{"questions": []string{"Which aspect?", "What scope?"}},
	}}
	inner, _ := json.Marshal(steps)
	outer, _ := json.Marshal(map[string]any{"text": string(inner)})
	sse := "data: " + string(outer) + "\n" + "data: {\"final\":true}\n"

	_, conv := askTestSetup(t, sse)
	_, err := conv.Ask(context.Background(), "q", AskOptions{})
	var cq *ClarifyingQuestionsError
	if !errors.As(err, &cq) {
		t.Fatalf("err = %v, want ClarifyingQuestionsError", err)
	}
	if len(cq.Questions) != 2 || cq.Questions[0] != "Which aspect?" || cq.Questions[1] != "What scope?" {
		t.Fatalf("questions = %v", cq.Questions)
	}
}

func TestAsk_FinalTerminatesHeldOpenStream(t *testing.T) {
	// Server sends the final frame and then holds the body open forever;
	// Ask must return promptly instead of waiting for the client timeout.
	released := make(chan struct{})
	ts := &askServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/search/new", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/rest/sse/perplexity_ask", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"backend_uuid\":\"u\",\"text\":\"{\\\"answer\\\":\\\"done\\\"}\"}\n\n")
		fmt.Fprint(w, "data: {\"final\":true}\n\n")
		flush.Flush()
		<-released
	})
	ts.srv = httptest.NewServer(mux)
	t.Cleanup(func() { close(released); ts.srv.Close() })

	c, err := transport.NewPlain(transport.Options{BaseURL: ts.srv.URL, APIVersion: "2.18", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sp := spec.Spec{BaseURL: ts.srv.URL, APIVersion: "2.18"}
	sp.Endpoints.SearchInit = "/search/new"
	sp.Endpoints.Ask = "/rest/sse/perplexity_ask"
	sp.Defaults = spec.Defaults{PromptSource: "user", SendBackTextInStreaming: true, Language: "en-US", SourceFocus: "web", SearchFocus: "internet"}
	sp.Models = []spec.Model{{Name: "best", Identifier: "pplx_pro", Mode: "copilot"}}
	conv := NewConversation(c, &sp)

	done := make(chan *Answer, 1)
	errCh := make(chan error, 1)
	go func() {
		ans, err := conv.Ask(context.Background(), "q", AskOptions{})
		done <- ans
		errCh <- err
	}()
	select {
	case ans := <-done:
		if err := <-errCh; err != nil {
			t.Fatalf("Ask: %v", err)
		}
		if ans.Text != "done" {
			t.Errorf("Text = %q", ans.Text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ask did not return after final frame; stream read is blocked")
	}
}

func TestAsk_CitationMarkersWithoutResults(t *testing.T) {
	// Out-of-range/zero-result citations: text keeps its [n] markers and
	// the answer is still returned (reference leaves the text alone when
	// there is no matching search result).
	inner, _ := json.Marshal(map[string]any{"answer": "See [1] and [99]."})
	outer, _ := json.Marshal(map[string]any{"text": string(inner)})
	sse := "data: " + string(outer) + "\ndata: {\"final\":true}\n"
	_, conv := askTestSetup(t, sse)
	ans, err := conv.Ask(context.Background(), "q", AskOptions{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Text != "See [1] and [99]." {
		t.Errorf("Text = %q, want markers preserved", ans.Text)
	}
	if len(ans.Citations) != 0 {
		t.Errorf("Citations = %+v, want none", ans.Citations)
	}
}

func TestAsk_AnswerBeatsEarlierChunks(t *testing.T) {
	// Later frame carrying a full answer must replace earlier chunk-derived
	// text (reference _update_state: answer overwrites, chunks append).
	chunkFrame, _ := json.Marshal(map[string]any{"text": `{"chunks":["partial "]}`})
	ansFrame, _ := json.Marshal(map[string]any{"text": `{"answer":"complete answer"}`})
	sse := "data: " + string(chunkFrame) + "\ndata: " + string(ansFrame) + "\ndata: {\"final\":true}\n"
	_, conv := askTestSetup(t, sse)
	ans, err := conv.Ask(context.Background(), "q", AskOptions{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Text != "complete answer" {
		t.Errorf("Text = %q, want complete answer", ans.Text)
	}
}
