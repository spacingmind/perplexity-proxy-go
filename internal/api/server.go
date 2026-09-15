// Package api implements an Anthropic Messages-compatible HTTP server over
// the pplx client: POST /v1/messages (non-streaming and SSE streaming).
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/spacingmind/perplexity-proxy-go/internal/pplx"
	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// ClientFactory mirrors the mcp seam: production wires token + transport,
// tests inject an httptest-backed fake.
type ClientFactory func() (transport.Client, *spec.Spec, error)

// Server serves the Anthropic-compatible API.
type Server struct {
	factory ClientFactory
	apiKey  string // empty = no auth (loopback assumption)

	mu    sync.Mutex
	convs map[string]*pplx.Conversation // conversationID -> live conversation
}

func New(factory ClientFactory, apiKey string) *Server {
	return &Server{factory: factory, apiKey: apiKey, convs: make(map[string]*pplx.Conversation)}
}

// Handler returns the HTTP handler (for httptest and ListenAndServe).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", s.handleMessages)
	return mux
}

// --- Anthropic wire shapes --------------------------------------------------

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type message struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []contentBlock `json:"content"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        usage          `json:"usage"`
}

func writeError(w http.ResponseWriter, status int, typ, msg string) {
	var eb struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	eb.Type = "error"
	eb.Error.Type = typ
	eb.Error.Message = msg
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(eb)
}

// --- request parsing --------------------------------------------------------

type messagesRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	System    string `json:"system"`
	Messages  []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

// flattenContent extracts the text of an Anthropic content value: a plain
// string or a list of {type:"text", text} blocks. Non-text blocks are an
// error — Perplexity has no image/tool pathway here.
func flattenContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("content must be a string or a list of text blocks")
	}
	var parts []string
	for _, b := range blocks {
		if b.Type != "text" {
			return "", fmt.Errorf("unsupported content block type %q (only text is supported)", b.Type)
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n"), nil
}

// mapModel resolves an Anthropic-style or native name against the spec's
// model list; unknowns pass through and pplx falls back to best.
func mapModel(sp *spec.Spec, m string) string {
	for _, mod := range sp.Models {
		if mod.Name == m || mod.Identifier == m {
			return m
		}
	}
	return m
}

// --- handler ----------------------------------------------------------------

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "POST only")
		return
	}
	if s.apiKey != "" {
		key := r.Header.Get("x-api-key")
		if key == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				key = strings.TrimPrefix(auth, "Bearer ")
			}
		}
		if key != s.apiKey {
			writeError(w, http.StatusUnauthorized, "authentication_error", "missing or invalid API key")
			return
		}
	}

	var req messagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "messages must contain at least one message")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	var turns []string
	for i, m := range req.Messages {
		text, err := flattenContent(m.Content)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error",
				fmt.Sprintf("messages[%d]: %v", i, err))
			return
		}
		role := m.Role
		if role != "user" && role != "assistant" {
			writeError(w, http.StatusBadRequest, "invalid_request_error",
				fmt.Sprintf("messages[%d]: role must be user or assistant, got %q", i, role))
			return
		}
		turns = append(turns, role+": "+text)
	}
	query := strings.Join(turns, "\n")
	if req.System != "" {
		query = "system: " + req.System + "\n" + query
	}

	t, sp, err := s.factory()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}

	// Conversation identity: explicit header wins; else one shared default
	// conversation so consecutive requests become followups.
	convID := r.Header.Get("x-pplx-conversation")
	if convID == "" {
		convID = "default"
	}
	conv := s.conversation(convID, t, sp)

	if req.Stream {
		s.serveStream(w, r, conv, query, pplx.AskOptions{Model: mapModel(sp, req.Model)}, req.Model)
		return
	}
	s.serveJSON(w, r, conv, query, pplx.AskOptions{Model: mapModel(sp, req.Model)}, req.Model)
}

// conversation returns the live conversation for convID, creating it on
// demand so the second request in the same conversation sends
// last_backend_uuid.
func (s *Server) conversation(convID string, t transport.Client, sp *spec.Spec) *pplx.Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	conv, ok := s.convs[convID]
	if !ok {
		conv = pplx.NewConversation(t, sp)
		s.convs[convID] = conv
	}
	return conv
}

func (s *Server) serveJSON(w http.ResponseWriter, r *http.Request, conv *pplx.Conversation, query string, opt pplx.AskOptions, model string) {
	ans, err := conv.Ask(r.Context(), query, opt)
	if err != nil {
		writeError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	text := ans.Text
	if text == "" {
		text = "(no answer returned)"
	}
	msg := message{
		ID:           "msg_" + ans.UUID,
		Type:         "message",
		Role:         "assistant",
		Model:        model,
		Content:      []contentBlock{{Type: "text", Text: text}},
		StopReason:   "end_turn",
		StopSequence: nil,
		Usage: usage{
			InputTokens:  estimateTokens(query),
			OutputTokens: estimateTokens(text),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(msg)
}

// serveStream emits the Anthropic SSE event sequence, driving
// content_block_delta from AskStream's chunk callback.
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, conv *pplx.Conversation, query string, opt pplx.AskOptions, model string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "api_error", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	send := func(event string, data any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	const blockIndex = 0
	send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_pending", "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": estimateTokens(query), "output_tokens": 0},
		},
	})
	send("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         blockIndex,
		"content_block": map[string]any{"type": "text", "text": ""},
	})

	ans, err := conv.AskStream(r.Context(), query, opt, func(chunk string) {
		send("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": blockIndex,
			"delta": map[string]any{"type": "text_delta", "text": chunk},
		})
	})
	if err != nil {
		send("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": err.Error()}})
		return
	}

	text := ans.Text
	if text == "" {
		text = "(no answer returned)"
	}
	send("content_block_stop", map[string]any{"type": "content_block_stop", "index": blockIndex})
	send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": estimateTokens(text)},
	})
	send("message_stop", map[string]any{"type": "message_stop"})
}

// estimateTokens approximates token counts (~4 chars/token); usage fields
// are estimates per the plan.
func estimateTokens(s string) int {
	n := len(s) / 4
	if n == 0 && s != "" {
		n = 1
	}
	return n
}
