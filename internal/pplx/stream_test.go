package pplx

import (
	"context"
	"strings"
	"testing"
)

// streamFixture mirrors the live wire shape: an ask_text block whose
// IN_PROGRESS frames carry only the chunks new since the previous frame
// (chunk_starting_offset tracks them), then a DONE frame repeating the full
// chunk list alongside the final answer.
const streamFixture = "data: {\"backend_uuid\":\"uuid-s\",\"thread_title\":\"Stream\"}\n" +
	"\n" +
	"data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"progress\":\"IN_PROGRESS\",\"chunks\":[\"Hel\"],\"chunk_starting_offset\":0}}]}\n" +
	"\n" +
	"data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"progress\":\"IN_PROGRESS\",\"chunks\":[\"lo \",\"wor\"],\"chunk_starting_offset\":1}}]}\n" +
	"\n" +
	"data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"progress\":\"IN_PROGRESS\",\"chunks\":[\"ld\"],\"chunk_starting_offset\":3}}]}\n" +
	"\n" +
	"data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"progress\":\"DONE\",\"chunks\":[\"Hel\",\"lo \",\"wor\",\"ld\"],\"chunk_starting_offset\":0,\"answer\":\"Hello world\"}}]}\n" +
	"\n" +
	"data: {\"final\":true}\n"

func TestAskStream_ChunksDelivered(t *testing.T) {
	ts, conv := askTestSetup(t, streamFixture)

	var got []string
	ans, err := conv.AskStream(context.Background(), "q", AskOptions{}, func(chunk string) {
		got = append(got, chunk)
	})
	if err != nil {
		t.Fatalf("AskStream: %v", err)
	}
	// deltas arrive in stream order; the DONE frame's repeated full list is
	// not forwarded (it would duplicate every delta).
	want := []string{"Hel", "lo ", "wor", "ld"}
	if len(got) != len(want) {
		t.Fatalf("chunks = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chunks[%d] = %q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}
	if ans.Text != "Hello world" {
		t.Errorf("final Text = %q, want Hello world", ans.Text)
	}
	if ans.UUID != "uuid-s" {
		t.Errorf("UUID = %q", ans.UUID)
	}
	// streaming did not skip the request body capture
	if bodies := ts.askBodies(); len(bodies) != 1 {
		t.Errorf("bodies = %d, want 1", len(bodies))
	}
}

func TestAskStream_FinalAnswerMatches(t *testing.T) {
	_, conv := askTestSetup(t, streamFixture)

	var streamed strings.Builder
	ans, err := conv.AskStream(context.Background(), "q", AskOptions{}, func(chunk string) {
		streamed.WriteString(chunk)
	})
	if err != nil {
		t.Fatalf("AskStream: %v", err)
	}
	// The concatenated deltas must reproduce the final answer text: the
	// Anthropic streaming layer (Phase 2 Step B/C) relies on this
	// invariant to emit content_block_delta events summing to the text.
	if streamed.String() != ans.Text {
		t.Errorf("joined deltas %q != final answer %q", streamed.String(), ans.Text)
	}

	// Non-streaming Ask on a fresh conversation must agree.
	_, conv2 := askTestSetup(t, streamFixture)
	ans2, err := conv2.Ask(context.Background(), "q", AskOptions{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans2.Text != ans.Text {
		t.Errorf("Ask(%q) != AskStream(%q)", ans2.Text, ans.Text)
	}
}

func TestAskStream_NilCallback(t *testing.T) {
	_, conv := askTestSetup(t, streamFixture)
	ans, err := conv.AskStream(context.Background(), "q", AskOptions{}, nil)
	if err != nil {
		t.Fatalf("AskStream nil callback: %v", err)
	}
	if ans.Text != "Hello world" {
		t.Errorf("Text = %q", ans.Text)
	}
}

func TestAskStream_StillFollowupAfter(t *testing.T) {
	ts, conv := askTestSetup(t, streamFixture)
	if _, err := conv.AskStream(context.Background(), "q1", AskOptions{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Ask(context.Background(), "q2", AskOptions{}); err != nil {
		t.Fatal(err)
	}
	bodies := ts.askBodies()
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	params := bodies[1]["params"].(map[string]any)
	if got := params["last_backend_uuid"]; got != "uuid-s" {
		t.Errorf("last_backend_uuid = %v, want uuid-s (streamed ask must capture uuid)", got)
	}
	if got := params["query_source"]; got != "followup" {
		t.Errorf("query_source = %v", got)
	}
}

func TestAskStream_RateLimitedMidStream(t *testing.T) {
	sse := "data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"progress\":\"IN_PROGRESS\",\"chunks\":[\"partial \"]}}]}\n" +
		"data: {\"error_code\":\"FREE_TIER_RATE_LIMITED\"}\n"
	_, conv := askTestSetup(t, sse)

	var chunks []string
	_, err := conv.AskStream(context.Background(), "q", AskOptions{}, func(s string) { chunks = append(chunks, s) })
	if err == nil || !strings.Contains(err.Error(), ErrRateLimited.Error()) {
		t.Fatalf("err = %v, want rate limit", err)
	}
	if len(chunks) != 1 {
		t.Errorf("chunks before failure = %q, want the one partial delta", chunks)
	}
}
