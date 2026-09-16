# perplexity-proxy-go

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Go port of [perplexity-web-mcp](https://github.com/jacob-bd/perplexity-web-mcp):
use a Perplexity Pro subscription from the terminal via the unofficial web API.

> Unofficial, not affiliated with or endorsed by Perplexity AI. Uses
> undocumented web APIs that may change or break without notice; use at
> your own risk and in compliance with Perplexity's terms of service.

## Build

```
GOFLAGS=-tags=http2legacy go build -o pplx ./cmd/pplx
```

The `http2legacy` tag is **required**: on Go 1.27+, x/net/http2 otherwise compiles
its `transport_wrap.go` (a wrapper around net/http internals), and the vendored
Chrome-fingerprint patch in `internal/http2x/transport.go` would be dead code.
See "Transport fingerprinting" below.

Or `source .env.build` then `go build ./...`.

## Commands

```
pplx login                        # email + OTP (TOTP supported)
pplx ask "query" [-m model] [-s source]
pplx usage                        # remaining rate limits
pplx spec show|sync|check         # protocol spec inspection / update
pplx mcp                          # stdio MCP server
pplx serve [--addr] [--api-key]   # Anthropic-compatible HTTP server
pplx logout                       # delete the local token
```

Models: `-m sonar|best|gpt56_terra|claude50sonnet|gemini31|grok45|glm|kimi|...`
(`pplx spec show` lists all).

## Anthropic-compatible server: `pplx serve`

```
pplx serve --addr 127.0.0.1:8080 --api-key mykey
```

Implements `POST /v1/messages` (Anthropic Messages API shape, streaming
included). Point any Anthropic-compatible client at it:

```
export ANTHROPIC_BASE_URL=http://127.0.0.1:8080
export ANTHROPIC_AUTH_TOKEN=mykey   # whatever --api-key you set
```

- Model names map to pplx models: `claude-sonnet-5` → `claude50sonnet`,
  `gpt-5.6` → `gpt56_terra`, etc.; native identifiers (`best`, `sonar`)
  pass through.
- Multi-turn: requests hitting the same server share one Perplexity
  conversation, so follow-ups carry context (send `x-pplx-conversation:
  <id>` to start/isolate threads).
- `stream: true` emits the Anthropic SSE sequence (`message_start` →
  `content_block_delta` per chunk → `message_stop`).
- System prompts become a `system:` prefix line; only text content blocks
  are supported.

## MCP server: `pplx mcp`

Stdio MCP server with tools `pplx_ask`, `pplx_deep_research`, `pplx_usage`,
`pplx_spec_check`. Claude Code:

```json
{ "mcpServers": { "pplx": { "command": "/path/to/pplx", "args": ["mcp"] } } }
```

Any MCP-capable client (Paseo, smind, ...) that speaks stdio JSON-RPC works
the same way — run `pplx mcp` as the server command.

## Transport fingerprinting

The ask endpoint bot-scores the TLS + HTTP/2 fingerprint. Passing requires:

1. **TLS ClientHello**: captured curl_cffi `impersonate=chrome` (Chrome 150)
   hello, embedded at `internal/transport/data/chrome150_clienthello.bin`
   (utls's newest builtin is Chrome 133 — too old).
2. **HTTP/2**: vendored `internal/http2x` (x/net/http2 v0.59.0) patched to send
   Chrome's exact SETTINGS (4 settings, exact order), Chrome's pseudo-header
   order `:method :authority :scheme :path`, Chrome's regular-header sequence,
   and **no** PRIORITY frames (Chrome dropped dependency priority).
3. **Headers**: curl_cffi-style set including navigation-style `sec-fetch-*`
   on POST (matches what the reference client sends; see `appHeaders`).

Verified by differential testing: the Python reference passes 6/6 with our
headers swapped in, proving the wire layer was the deciding signal; the Go
client now passes 8/8 live.

## Spec-driven protocol updates

Protocol constants live in `internal/spec/data/spec.json` (endpoints, API
version, model identifiers), overridable at runtime via
`~/.pplx/spec-overrides.json` or `PPLX_*` env. `pplx spec sync` fetches the
upstream Python constants and diffs them for review before writing.

## Development

```
GOFLAGS=-tags=http2legacy go test ./...
```

No test touches the network; live smoke tests are manual (`pplx dump`).

## Bridge fallback (recommended setup)

The Go transport's fingerprint occasionally gets authwalled by Perplexity's
bot scoring. `pplx` auto-falls back to a curl_cffi bridge (the reference
engine, passes consistently):

```
pplx bridge setup     # creates ~/.pplx/bridge-venv (python3 + curl_cffi)
```

After setup, no env vars needed. Controls: `PPLX_BRIDGE=1` (always bridge),
`PPLX_NO_BRIDGE=1` (never), `PPLX_PYTHON` (custom python), `PPLX_BRIDGE_SCRIPT`
(custom bridge script).

Note: the reference Python package must be importable by the bridge script —
it points at a sibling checkout (`refs/perplexity-web-mcp`); adjust
`scripts/bridge_ask.py` `sys.path` if your layout differs.
