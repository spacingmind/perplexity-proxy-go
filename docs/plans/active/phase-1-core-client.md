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

- [ ] Phase 1 skeleton + spec embedded
- [ ] Auth login
- [ ] Ask + SSE
- [ ] Usage + spec sync/check
- [ ] Test suite xanh

## Validation

(điền khi hoàn thành: kết quả từng acceptance criterion)
