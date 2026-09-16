# Contributing

## Development

```
source .env.build            # GOFLAGS=-tags=http2legacy (REQUIRED on Go 1.27+)
go build ./cmd/pplx
go test ./...
go vet ./...
```

The `http2legacy` build tag is mandatory: it selects the vendored
`internal/http2x` standalone transport (with the Chrome fingerprint patch)
instead of x/net/http2's net/http wrapper, where the patch would be dead
code. See README "Transport fingerprinting".

## Ground rules

- Tests must not hit the live Perplexity API — fixtures and httptest only.
  Live smoke tests are manual and burn Pro Search quota.
- Commits follow [Conventional Commits](https://www.conventionalcommits.org/)
  (`feat:`, `fix:`, `chore:`, ...); releases are automated via
  release-please from the master history.
- PRs target `master`; keep them small and described via the template.
- Never commit session tokens, captured cookies, or other credentials.
- This is an unofficial client for undocumented APIs — expect breakage;
  protocol fixes belong in `internal/spec` (data) first, code second.

## Legal

By contributing you agree your contributions are licensed under the MIT
license (see LICENSE, including third-party notices).
