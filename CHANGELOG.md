# Changelog

All notable changes to this project are documented in this file.

Commits from this point forward should use [Conventional
Commits](https://www.conventionalcommits.org/) so `release-please` can
generate this file automatically; entries before `v0.1.0` were written by
hand since the early history predates that convention.

## [0.1.0] - 2026-09-16

Initial release. A Go client, MCP server, and Anthropic-compatible HTTP
proxy for Perplexity's unofficial web API, built to make a Perplexity Pro
subscription usable from the terminal and from any Anthropic-Messages-API
client (including [smind](https://github.com/spacingmind/smind)).

### Added

- **Core client**: email + OTP/TOTP login with an expiring local token
  store; `ask` with defensive SSE parsing, follow-up/thread state, source
  filters, and rate-limit reporting.
- **Protocol spec system**: endpoints, API version, and model identifiers
  live in an embedded JSON spec, overridable at runtime via
  `~/.pplx/spec-overrides.json` or `PPLX_*` env vars; `pplx spec sync`
  diffs against the upstream Python reference for review before applying
  drift; `pplx spec check` smoke-tests each endpoint.
- **CLI**: `login`, `ask`, `usage`, `spec show|sync|check`, `logout`.
- **Browser-fingerprinted transport**: a captured Chrome 150 TLS
  ClientHello (via `utls`) and a vendored, byte-exact Chrome HTTP/2 frame
  layer (`internal/http2x`, forked from `x/net/http2`) — required because
  Perplexity's ask endpoint bot-scores the TLS/H2 fingerprint, and Go's
  stdlib/default `x/net/http2` shape gets rejected.
- **Bridge fallback**: when the Go transport's fingerprint gets authwalled,
  `pplx` automatically falls back to a `curl_cffi` bridge (the reference
  engine, which the site never rejects). `pplx bridge setup` provisions a
  managed Python venv so no manual environment configuration is needed.
- **MCP server** (`pplx mcp`): stdio JSON-RPC server exposing `ask`,
  `deep-research`, `usage`, and `spec-check` as tools for any MCP client
  (Claude Code, Paseo, smind, ...).
- **Anthropic-compatible HTTP server** (`pplx serve`): implements
  `POST /v1/messages` (including streaming SSE), so any tool that speaks
  the Anthropic Messages API can point `ANTHROPIC_BASE_URL` at it and use
  Perplexity Pro as a model provider.

### Fixed (via adversarial review before first release)

- TOTP resume retried by re-consuming the original OTP instead of
  re-challenging.
- `search_recency_filter` sent as `""` instead of omitted/`null` when
  unset.
- Query truncation cut on raw bytes, corrupting multi-byte (CJK) runes.
- `RESEARCH_CLARIFYING_QUESTIONS` frames were dropped instead of surfaced.
- Stream reader didn't stop on the true terminator
  (`final_sse_message`, not `final`), sometimes hanging or truncating
  answers.
- Session cookies weren't scoped to the request host, risking cross-host
  leakage on redirects.
- `spec sync` had no timeout on the upstream fetch.
- SSE line buffer had no size cap.

[0.1.0]: https://github.com/spacingmind/perplexity-proxy-go/releases/tag/v0.1.0
