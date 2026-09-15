package pplx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// Fixture: authwall response (upsell present, completed answer).
func TestAsk_AuthwallSurfacesError(t *testing.T) {
	fixture := strings.Join([]string{
		`data: {"upsell_information": {"name": "fraud_authwall_upsell"}, "backend_uuid": "u1", "blocks": [{"intended_usage": "ask_text", "markdown_block": {"progress": "IN_PROGRESS", "chunks": ["Sign"]}}]}`,
		`data: {"upsell_information": {"name": "fraud_authwall_upsell"}, "blocks": [{"intended_usage": "ask_text", "markdown_block": {"progress": "DONE", "answer": "Sign up and repeat your request.", "chunks": ["Sign"]}}], "final": true, "final_sse_message": true}`,
		`data: {}`,
		"", "",
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(fixture))
	}))
	defer srv.Close()
	sp := &spec.Spec{BaseURL: srv.URL, APIVersion: "2.18", Endpoints: spec.Endpoints{Ask: "/ask", SearchInit: "/init"}, Defaults: spec.Defaults{SearchFocus: "internet", Language: "en-US", SourceFocus: "web", PromptSource: "user", SendBackTextInStreaming: true, SaveToLibrary: false}, Models: []spec.Model{{Name: "best", Identifier: "pplx_pro", Mode: "copilot"}}}
	tc, _ := transport.NewPlain(transport.Options{BaseURL: srv.URL})
	conv := NewConversation(tc, sp)
	_, err := conv.Ask(context.Background(), "hi", AskOptions{})
	if err == nil || !strings.Contains(err.Error(), "authwall") {
		t.Fatalf("want authwall error, got %v", err)
	}
	t.Log("authwall surfaced:", err)
}
