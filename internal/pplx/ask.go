package pplx

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// AskOptions tunes a single query.
type AskOptions struct {
	Model       string // spec model name or identifier ("" -> spec default)
	SourceFocus string // e.g. "web", "scholar" ("" -> spec default)
}

// Answer is the result of one Ask call.
type Answer struct {
	Text        string
	Citations   []Citation
	ThreadTitle string
	UUID        string // conversation backend uuid for follow-ups
}

// Conversation runs queries against the ask endpoint, carrying thread state
// between calls so the second Ask is sent as a follow-up.
type Conversation struct {
	t              transport.Client
	sp             *spec.Spec
	backendUUID    string
	readWriteToken string
}

func NewConversation(t transport.Client, sp *spec.Spec) *Conversation {
	return &Conversation{t: t, sp: sp}
}

// RestoreSession injects prior thread state (uuid, optional read_write_token).
func (c *Conversation) RestoreSession(backendUUID, readWriteToken string) {
	c.backendUUID = backendUUID
	c.readWriteToken = readWriteToken
}

// buildPayload mirrors the reference _build_payload: the POST body is
// {"params": {...}, "query_str": query}.
func (c *Conversation) buildPayload(query string, opt AskOptions) map[string]any {
	d := c.sp.Defaults
	model := c.sp.Model(opt.Model)

	sourceFocus := opt.SourceFocus
	if sourceFocus == "" {
		sourceFocus = d.SourceFocus
	}
	recency := d.SearchRecencyFilter
	if recency == "" {
		recency = ""
	}

	params := map[string]any{
		"attachments":                     []string{},
		"language":                        d.Language,
		"timezone":                        currentTimezone(),
		"client_coordinates":              nil,
		"sources":                         []string{sourceFocus},
		"model_preference":                model.Identifier,
		"mode":                            model.Mode,
		"search_focus":                    d.SearchFocus,
		"search_recency_filter":           recency,
		"is_incognito":                    !d.SaveToLibrary,
		"use_schematized_api":             d.UseSchematizedAPI,
		"local_search_enabled":            false,
		"prompt_source":                   d.PromptSource,
		"send_back_text_in_streaming_api": d.SendBackTextInStreaming,
		"version":                         c.sp.APIVersion,
	}
	if c.backendUUID != "" {
		params["last_backend_uuid"] = c.backendUUID
		params["query_source"] = "followup"
		if c.readWriteToken != "" {
			params["read_write_token"] = c.readWriteToken
		}
	}
	return map[string]any{"params": params, "query_str": query}
}

// Ask initializes the search session (GET search_init?q=..., truncated to
// 500 chars like the reference), streams the ask SSE, and folds the result
// into an Answer. State captured from the stream (backend uuid, token) is
// kept so the next Ask is a follow-up.
func (c *Conversation) Ask(ctx context.Context, query string, opt AskOptions) (*Answer, error) {
	payload := c.buildPayload(query, opt)

	searchQuery := query
	if len(searchQuery) > 500 {
		searchQuery = searchQuery[:500]
	}
	if err := c.t.Get(ctx, fmt.Sprintf("%s?q=%s", c.sp.Endpoints.SearchInit, url.QueryEscape(searchQuery))); err != nil {
		return nil, fmt.Errorf("init search: %w", err)
	}

	state := &convState{}
	err := c.t.PostSSE(ctx, c.sp.Endpoints.Ask, payload, func(line []byte) error {
		d, ok := sseData(line)
		if !ok {
			return nil
		}
		return state.processData(d)
	})
	if err != nil {
		return nil, fmt.Errorf("ask: %w", err)
	}

	if state.backendUUID != "" {
		c.backendUUID = state.backendUUID
	}
	if state.readWriteToken != "" {
		c.readWriteToken = state.readWriteToken
	}

	return &Answer{
		Text:        state.answer,
		Citations:   state.citations,
		ThreadTitle: state.title,
		UUID:        c.backendUUID,
	}, nil
}

func currentTimezone() string {
	name, _ := time.Now().Zone()
	if name == "" {
		return "UTC"
	}
	if !strings.Contains(name, "/") {
		// Go returns abbreviations like "ICT"; the API expects IANA names.
		return "UTC"
	}
	return name
}
