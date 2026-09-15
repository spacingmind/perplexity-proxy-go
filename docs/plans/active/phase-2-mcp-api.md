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
