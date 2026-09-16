## What changed and why

<!-- Describe the change and the motivation behind it. -->

## Checklist

- [ ] `GOFLAGS=-tags=http2legacy go build ./... && go test ./... && go vet ./...` pass.
- [ ] PR title follows Conventional Commits (`feat:`, `fix:`, `chore:`, ...).
- [ ] Tests do not hit the live Perplexity API (fixtures/httptest only);
      live smoke tests are manual and quota-limited.
