package pplx

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// ErrRateLimited is returned when the stream carries a rate-limit error code.
var ErrRateLimited = errors.New("Perplexity rate limit reached (FREE_TIER_RATE_LIMITED)")

// ClarifyingQuestionsError is returned when the model asks clarifying
// questions instead of an answer (RESEARCH_CLARIFYING_QUESTIONS step).
type ClarifyingQuestionsError struct {
	Questions []string
}

func (e *ClarifyingQuestionsError) Error() string {
	return "Perplexity returned clarifying questions: " + strings.Join(e.Questions, " | ")
}

// sseData parses one SSE line ("data: {...}") into a generic map.
// Lines that are not data frames or that fail to parse are ignored — the
// stream may contain comments, keepalives, and unknown frames we must skip.
func sseData(line []byte) (map[string]any, bool) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil, false
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, false
	}
	return m, true
}

// convState accumulates conversation state across SSE events, mirroring the
// reference _update_state: latest answer text, chunks, citations, thread
// identity for follow-ups.
type convState struct {
	backendUUID    string
	readWriteToken string
	title          string
	answer         string
	chunks         []string
	citations      []Citation
	sawFinal       bool
}

// Citation is one web result cited by [n] markers in the answer text.
type Citation struct {
	Index int    `json:"index"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// processData folds one SSE data object into the state. Every field access
// is type-guarded; unknown fields and shapes are ignored without error so
// upstream protocol additions cannot break parsing.
func (s *convState) processData(d map[string]any) error {
	if ec, _ := d["error_code"].(string); ec == "FREE_TIER_RATE_LIMITED" {
		return ErrRateLimited
	}
	if v, ok := d["backend_uuid"].(string); ok && v != "" {
		s.backendUUID = v
	}
	if v, ok := d["read_write_token"].(string); ok && v != "" {
		s.readWriteToken = v
	}
	if v, ok := d["thread_title"].(string); ok && v != "" {
		s.title = v
	}
	if v, ok := d["final"].(bool); ok && v {
		s.sawFinal = true
	}

	// Primary shape: {"text": "<json string>"}.
	if text, ok := d["text"].(string); ok {
		return s.absorbText(text)
	}
	// Alternative shape: {"blocks": [...]}.
	if blocks, ok := d["blocks"].([]any); ok {
		s.absorbBlocks(blocks)
	}
	return nil
}

// absorbText parses the JSON payload carried in the "text" field: either a
// list of steps (one with step_type "FINAL") or a bare answer object.
func (s *convState) absorbText(text string) error {
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		// Non-JSON text frames are ignored rather than fatal.
		return nil
	}
	switch v := parsed.(type) {
	case []any:
		for _, item := range v {
			step, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if st, _ := step["step_type"].(string); st == "RESEARCH_CLARIFYING_QUESTIONS" {
				return &ClarifyingQuestionsError{Questions: extractClarifyingQuestions(step["content"])}
			}
			if st, _ := step["step_type"].(string); st == "FINAL" {
				content, _ := step["content"].(map[string]any)
				if content != nil {
					s.absorbAnswerData(content)
				}
				break
			}
		}
	case map[string]any:
		s.absorbAnswerData(v)
	}
	return nil
}

// absorbAnswerData extracts answer/web_results/chunks from an answer object.
func (s *convState) absorbAnswerData(data map[string]any) {
	if results, ok := data["web_results"].([]any); ok {
		var citations []Citation
		for i, item := range results {
			r, ok := item.(map[string]any)
			if !ok {
				continue
			}
			citations = append(citations, Citation{
				Index: i + 1,
				Title: stringOr(r["name"]),
				URL:   stringOr(r["url"]),
			})
		}
		if citations != nil {
			s.citations = citations
		}
	}
	if a, ok := data["answer"].(string); ok {
		s.answer = a
	}
	if chunks, ok := data["chunks"].([]any); ok {
		for _, c := range chunks {
			if cs, ok := c.(string); ok {
				s.chunks = append(s.chunks, cs)
			}
		}
		if s.answer == "" && len(s.chunks) > 0 {
			s.answer = strings.Join(s.chunks, "")
		}
	}
	if t, ok := data["thread_title"].(string); ok && t != "" {
		s.title = t
	}
}

// absorbBlocks handles the block-stream shape: blocks with intended_usage
// "ask_text" (markdown_block.answer/.chunks) and "web_results"
// (web_result_block.web_results).
func (s *convState) absorbBlocks(blocks []any) {
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		switch usage, _ := block["intended_usage"].(string); usage {
		case "ask_text":
			if md, ok := block["markdown_block"].(map[string]any); ok {
				s.absorbAnswerData(md)
			}
		case "web_results":
			if wr, ok := block["web_result_block"].(map[string]any); ok {
				if results, ok := wr["web_results"].([]any); ok {
					var citations []Citation
					for i, item := range results {
						r, ok := item.(map[string]any)
						if !ok {
							continue
						}
						citations = append(citations, Citation{
							Index: i + 1,
							Title: stringOr(r["name"]),
							URL:   stringOr(r["url"]),
						})
					}
					if citations != nil {
						s.citations = citations
					}
				}
			}
		}
	}
}

func stringOr(v any) string {
	s, _ := v.(string)
	return s
}

// extractClarifyingQuestions mirrors the reference _extract_clarifying_questions.
func extractClarifyingQuestions(content any) []string {
	var questions []string
	switch c := content.(type) {
	case map[string]any:
		if raw, ok := c["questions"].([]any); ok {
			for _, q := range raw {
				if s := stringOr(q); s != "" {
					questions = append(questions, s)
				}
			}
		} else if raw, ok := c["clarifying_questions"].([]any); ok {
			for _, q := range raw {
				if s := stringOr(q); s != "" {
					questions = append(questions, s)
				}
			}
		} else {
			for _, v := range c {
				if s, ok := v.(string); ok && strings.Contains(s, "?") {
					questions = append(questions, s)
				}
			}
		}
	case []any:
		for _, q := range c {
			if s := stringOr(q); s != "" {
				questions = append(questions, s)
			}
		}
	case string:
		if c != "" {
			questions = append(questions, c)
		}
	}
	return questions
}
