# Phase 2 — MCP server + Anthropic-compatible API endpoint

Goal: expose the Phase-1 client (`internal/pplx`) as (a) a stdio MCP server and
(b) an Anthropic Messages-compatible local HTTP endpoint, so Claude Code /
smind / any ACP agent can use the Perplexity Pro subscription as a provider.

## Design decisions

1. **MCP server** (`pplx mcp`): stdio transport, JSON-RPC 2.0. Tools:
   - `pplx_ask(query, model?, source_focus?)` → answer + citations
   - `pplx_deep_research(query)` → report (model `deep-research`)
   - `pplx_usage()` → remaining quotas
   - `pplx_spec_check()` → endpoint health
   No SDK dependency — hand-rolled JSON-RPC is ~200 lines for the subset MCP
   needs (initialize, tools/list, tools/call, notifications). MCP spec
   reference: refs/agent-client-protocol is ACP not MCP; use the MCP spec
   knowledge in-repo (JSON-RPC framing, `tools/call` result shapes).
2. **API server** (`pplx serve`): Anthropic `/v1/messages` compatible:
   - Request: model, max_tokens (ignored/capped), messages[] (system +
     user/assistant turns), stream flag
   - Maps conversation turns to `Conversation.Ask` followups (multi-turn via
     backend_uuid state per API conversation)
   - Response: Anthropic message shape (content blocks, stop_reason,
     usage stubs); `stream: true` → SSE events (message_start, content_block_delta,
     message_stop) driven by the SSE chunks from Ask — map incrementally where
     the parser exposes chunks, else emit one final block
   - Auth: optional bearer token flag (`--api-key`), loopback bind by default
3. **Streaming**: extend `internal/pplx` with `AskStream(ctx, query, opt,
   onChunk func(text string))` reusing the existing SSE parser but surfacing
   chunk deltas (parser already collects chunks; add a callback).
4. **Model name mapping**: map Anthropic-style names (`claude-sonnet-5` etc.)
   → pplx models (`claude50sonnet`) in the API server layer, plus pass-through
   of native identifiers.

## Acceptance criteria

1. `pplx mcp` speaks MCP over stdio: an MCP client handshake (initialize →
   tools/list → tools/call) works end-to-end in a test using an in-memory
   pipe, with a fake transport Client injected (no network).
2. `pplx serve` implements POST /v1/messages: non-streaming responds with a
   valid Anthropic message JSON for a single-turn request (httptest + fake
   client).
3. Streaming mode emits well-formed Anthropic SSE events (event: lines + JSON
   data), terminating with message_stop.
4. Multi-turn: second request in the same conversation arrives at the pplx
   client as a followup (last_backend_uuid set) — per-conversation state map.
5. API key enforcement: with --api-key set, requests without the bearer are
   401; without the flag, no auth required (loopback assumption).
6. Build still requires `-tags http2legacy`; README updated with `pplx mcp`
   (incl. Claude Code / Paseo / smind wiring snippets) and `pplx serve` usage
   (ANTHROPIC_BASE_URL=http://localhost:8080).
7. gofmt/vet/tests green (all with fakes; zero real network).

## Test scenarios (named)

- TestMCP_InitializeHandshake — initialize + tools/list shapes.
- TestMCP_ToolsCallAsk — tools/call returns answer + citations content.
- TestAPI_Messages_SingleTurn — request→Anthropic response mapping.
- TestAPI_Messages_Stream — SSE event sequence well-formed, message_stop last.
- TestAPI_Messages_MultiTurnFollowup — second call carries last_backend_uuid.
- TestAPI_AuthRequired — 401 without bearer when --api-key set.
- TestAPI_ModelMapping — claude-sonnet-5 → claude50sonnet identifier in payload.

## Out of scope (Phase 3 candidates)

- Council (multi-model), file uploads, thread listing endpoints in MCP.
- OpenAI /v1/chat/completions compat layer.
- TLS on the local server.

## Progress

- [x] Step A — streaming support in internal/pplx (AskStream + chunk callback) — commit 1bc82e8
- [x] Step B — MCP stdio server + pplx mcp wiring — commit 67bf9c2
- [x] Step C — Anthropic /v1/messages server + pplx serve wiring + README

## Validation

1. **MCP handshake end-to-end** — PASS. TestMCP_InitializeHandshake covers
   initialize → tools/list (4 tools, JSON Schema inputSchema, required
   fields); binary smoke `echo ... | pplx mcp` returns the initialize
   result. In-memory pipes + httptest fake transport, no network.
2. **POST /v1/messages single-turn** — PASS. TestAPI_Messages_SingleTurn:
   valid Anthropic message JSON (id msg_<uuid>, content blocks, end_turn,
   usage stubs). httptest + fake client.
3. **Streaming SSE** — PASS. TestAPI_Messages_Stream: exact event sequence
   message_start, content_block_start, content_block_delta x2,
   content_block_stop, message_delta, message_stop (last); joined
   text_deltas == final answer.
4. **Multi-turn followup** — PASS. TestAPI_Messages_MultiTurnFollowup:
   second request to the same conversation carries last_backend_uuid +
   query_source=followup (asserted on the captured wire payload);
   TestAPI_ConversationHeaderIsolation: x-pplx-conversation ids isolate
   threads. In-memory map conversationID→Conversation; explicit header or
   shared "default".
5. **API key enforcement** — PASS. TestAPI_AuthRequired: no key → 401
   Anthropic error shape; x-api-key and Bearer both accepted; wrong key →
   401. No flag → no auth.
6. **Build tag + README** — PASS. All build/test commands run with
   -tags http2legacy (source .env.build); README documents pplx serve
   (ANTHROPIC_BASE_URL) and pplx mcp (Claude Code / stdio config snippet).
7. **Tooling** — PASS. gofmt clean, go vet -tags http2legacy ./... clean,
   go test -tags http2legacy ./... green: cmd/pplx, internal/api (12),
   internal/mcp (10), internal/pplx, internal/spec, internal/transport.
   Zero real network in tests.

### Notes / deviations

- System prompt: flattened as a `system:` prefix line (plan's "keep
  simple" option).
- Multi-turn within one request: turns joined with role prefixes
  ("user: X\nassistant: Y\nuser: Z"); cross-request continuity via
  per-conversation Conversation instances (backend_uuid followups).
- usage tokens are ~len/4 estimates, as the plan allows ("usage stubs").
- max_tokens accepted but not enforced (Perplexity has no knob).
- Anthropic model names ARE the spec model names (claude-sonnet-5 etc), so
  "mapping" is a spec lookup; unknown names pass through and fall back to
  best inside pplx.
