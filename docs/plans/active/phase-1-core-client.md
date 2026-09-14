# Perplexity Proxy Go — port perplexity-web-mcp sang Go

Repo tham khảo: `refs/perplexity-web-mcp` trong smind (read-only, không copy code
wholesale — chỉ dùng làm nguồn giao thức).

## Mục tiêu

CLI Go `pplx` dùng Perplexity Pro subscription qua web API không chính thức:
`login`, `ask`, `usage`. Phase sau: MCP server + Anthropic-compatible API server.

## Nguyên tắc kiến trúc: tách spec khỏi code

Toàn bộ hằng số giao thức chỉ tồn tại ở một chỗ — `internal/spec` (data thuần,
không logic). Client (`internal/pplx`) đọc mọi thứ qua spec, không hardcode.

### Cấu trúc

- `cmd/pplx/` — CLI entrypoint
- `internal/spec/` — spec JSON nhúng + struct Go; overrides runtime
- `internal/transport/` — HTTP client với TLS fingerprint (utls)
- `internal/pplx/` — client logic: auth, ask, rate limits, SSE parse

### Cơ chế giảm rủi ro giao thức (từ plan đã duyệt)

1. **Runtime override**: `~/.pplx/spec-overrides.json` + env (`PPLX_API_VERSION`)
   đè lên spec nhúng — sửa drift không cần recompile.
2. **`pplx spec sync`**: fetch `constants.py` + `models.py` từ GitHub upstream
   (jacob-bd/perplexity-web-mcp), parse, diff với spec local, in thay đổi chờ
   duyệt rồi mới ghi.
3. **`pplx spec check`**: smoke test rate-limits → ask → thread list, báo endpoint
   nào vỡ.
4. **Parse phòng thủ**: SSE parse bằng `map[string]any`, trích field cần thiết,
   bỏ qua field lạ.

## Spec ban đầu (port từ constants.py @ upstream HEAD)

- BaseURL: `https://www.perplexity.ai`
- APIVersion: `2.18`
- Endpoints: ask `/rest/sse/perplexity_ask`, search init `/search/new`,
  upload `/rest/uploads/batch_create_upload_urls`, rate limits `/rest/rate-limit/all`,
  user settings `/rest/user/settings`, thread list `/rest/thread/list_ask_threads`,
  thread detail `/rest/thread`, credits `/rest/billing/credits`
- Auth: CSRF + signin OTP flow (auth.py), cookie `__Secure-next-auth.session-token`
- Models: identifier map (auto, experimental=Sonar, pplx_alpha=deep research,
  gpt56_terra, claude50sonnet, gemini31pro_high, grok45low, glm, kimi...)
- Headers: x-app-apiversion = APIVersion, browser-like headers

## Phase 1 — Acceptance criteria

1. `pplx login` (email + OTP, hỗ trợ TOTP) lưu token vào `~/.pplx/token.json`,
   token có expiry check (~30 ngày).
2. `pplx ask "query" [-m model] [-s source]` in answer + citations, exit 0.
3. `pplx usage` in rate limit còn lại.
4. `pplx spec show|sync|check` hoạt động: show in spec effective; sync diff
   upstream; check smoke test 3 endpoint.
5. SSE streaming parse không vỡ khi response có field lạ (test với fixture
   có field lạ).
6. `internal/transport` dùng utls ClientHello browser-like; tách interface để
   thay được.
7. `go vet`, `gofmt`, `go test ./...` xanh; test không gọi mạng thật (dùng
   httptest + fixture), trừ smoke test `spec check` (opt-in flag).

## Test scenarios (đặt tên)

- `TestLogin_OTPFlow` — mock CSRF/signin/verify, assert token file + expiry.
- `TestAsk_StreamsAnswer` — fixture SSE, assert answer text + citations.
- `TestAsk_IgnoresUnknownFields` — fixture SSE lẫn field lạ, không panic, vẫn
   trích được answer.
- `TestSpec_OverridesPrecedence` — embedded < file override < env.
- `TestSpecSync_DetectsVersionBump` — mock upstream constants.py 2.19, assert
   diff hiển thị.
- `TestUsage_ParsesRateLimits` — fixture JSON rate-limit.
- `TestFollowup_SendsBackendUUID` — conversation thứ 2 gửi last_backend_uuid.

## Decisions

- Tên repo `perplexity-proxy-go`, binary `pplx` (2026-09-14).
- TLS fingerprint qua refraction-networking/utls, giấu trong internal/transport.
- Spec nhúng dạng JSON trong `internal/spec/data/`, đi kèm struct Go typed.

## Progress

- [x] Phase 1 skeleton + spec embedded (commit 193694b)
- [x] Auth login (df13369)
- [x] Ask + SSE (13b5127)
- [x] Usage + spec sync/check (b29a81a, this commit)
- [x] Test suite xanh (39 tests, 4 packages)

## Validation

1. **`pplx login`** — PASS. Email+OTP two-phase flow (`RequestCode` ->
   `CompleteLogin`), TOTP challenge handled with re-prompt; saves
   `~/.pplx/token.json` (0700/0600) with `expires_at` ~30d; `Load` fails
   with `ErrNoToken`/`ErrTokenExpired`. Tests: `TestLogin_OTPFlow`,
   `TestLogin_TOTPRequired`, `TestLogin_BadCode`, `TestCLI_LoginFlow`.
2. **`pplx ask "query" [-m] [-s]`** — PASS. Answer + numbered Sources list;
   `--no-citations`; trailing flags after the query parsed in a second
   pass. Tests: `TestAsk_StreamsAnswer`, `TestAsk_AnswerFieldShape`,
   `TestCLI_AskPrintsCitations`, `TestCLI_AskNoCitations`.
3. **`pplx usage`** — PASS. Four buckets + capped sources, unlimited
   omitted. Tests: `TestUsage_ParsesRateLimits`, `TestCLI_Usage`.
4. **`pplx spec show|sync|check`** — PASS. show prints effective spec +
   applied-override header; sync fetches upstream constants.py/models.py,
   regex-parses `Final[str]`/`Model(...)`, diffs, confirm-before-write
   (`--yes` skips), merges into existing overrides (only changed keys
   persisted); check dry-run prints the 3-step plan with no network,
   `--live` smoke tests rate-limits -> ask -> thread-list with
   PASS/FAIL + HTTP status. Tests: `TestSpecSync_DetectsVersionBump`
   (2.19 bump + moved endpoint; written on --yes, not on decline),
   `TestSpecSync_NoDrift`, `TestSpecShow`, `TestSpecCheck_DryRunNoNetwork`.
5. **Defensive SSE parse** — PASS. All frames -> `map[string]any`,
   type-guarded extraction, unknown fields/events/wrong types/non-JSON
   skipped without panic; both `{text: json}` and `{blocks}` shapes.
   Tests: `TestAsk_IgnoresUnknownFields` + hostile fixture in
   `TestAsk_StreamsAnswer`.
6. **utls transport behind interface** — PASS.
   `transport.Client` interface with `NewPlain` (tests) / `NewUTLS`
   (Chrome ClientHello, h2 via ForceAttemptHTTP2) implementations;
   CLI reaches it through the `newTransport` seam. Tests:
   `TestInterfaceSatisfied` + 10 transport tests (headers, cookies,
   chunked cookies, SSE, redirects).
7. **Tooling** — PASS. `gofmt -l .` empty, `go vet ./...` clean,
   `go test ./...` ok (39 tests: spec 6, transport 11, pplx 12, cli 12 —
   minus one shared helper, 39 named). No network in tests: every HTTP
   interaction goes through httptest servers; `spec check --live` is
   opt-in and its default mode asserts zero network calls.

**Named test scenarios all present:** TestLogin_OTPFlow, TestAsk_StreamsAnswer,
TestAsk_IgnoresUnknownFields, TestSpec_OverridesPrecedence,
TestSpecSync_DetectsVersionBump, TestUsage_ParsesRateLimits,
TestFollowup_SendsBackendUUID.

### Deviations from plan

- CLI is stdlib-only (no cobra) — flag parsing with a second pass for
  flags-after-query; per plan's "minimal arg parsing" allowance.
- `.gitignore` `pplx` pattern anchored to `/pplx` — the bare pattern was
  silently ignoring the `internal/pplx/` package directory.
- Dependency set: `refraction-networking/utls` (+ its transitive deps)
  only; stdlib covers HTTP/2, JSON, regex, SSE scanning. `x/net/http2`
  was evaluated then dropped in favour of stdlib ForceAttemptHTTP2.
- Token path overridable via `PPLX_TOKEN_PATH` (test seam, also handy
  for users).
- SSE `chunks` accumulate by join (matches reference `_update_state`);
  later `answer` fields win over earlier chunk-derived text.
