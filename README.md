# perplexity-proxy-go

Go port of [perplexity-web-mcp](https://github.com/jacob-bd/perplexity-web-mcp):
use a Perplexity Pro subscription from the terminal via the unofficial web API.

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
pplx logout                       # delete the local token
```

Models: `-m sonar|best|gpt56_terra|claude50sonnet|gemini31|grok45|glm|kimi|...`
(`pplx spec show` lists all).

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
