package pplx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// authTestServer mocks the Perplexity auth endpoints from auth.py:
// warm-up, csrf, signin/email, otp-redirect-link, callback redirect,
// optional TOTP challenge, and final session cookie.
type authTestServer struct {
	srv          *httptest.Server
	client       *recordingClient
	totpRequired bool

	csrfCalls        int
	signinCalls      int
	otpRedirectCalls int
	totpCalls        int
	callbackCalls    int
	lastSigninBody   map[string]any
	lastTOTPBody     map[string]any
}

type recordingClient struct {
	transport.Client
}

func newAuthTestServer(t *testing.T, totpRequired bool) *authTestServer {
	t.Helper()
	ts := &authTestServer{totpRequired: totpRequired}
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			// warm-up visit
			fmt.Fprint(w, "<html>perplexity</html>")
		case "/api/auth/csrf":
			ts.csrfCalls++
			fmt.Fprint(w, `{"csrfToken":"csrf-123"}`)
		case "/api/auth/signin/email":
			ts.signinCalls++
			ts.lastSigninBody = decodeBody(t, r)
			fmt.Fprint(w, `{}`)
		case "/api/auth/otp-redirect-link":
			ts.otpRedirectCalls++
			fmt.Fprint(w, `{"redirect":"/api/auth/callback/login-web-otp?uuid=abc"}`)
		case "/api/auth/callback/login-web-otp":
			ts.callbackCalls++
			if ts.totpRequired {
				http.Redirect(w, r, "/auth/totp-challenge?token=challenge-tok", http.StatusFound)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     "__Secure-next-auth.session-token",
				Value:    "session-token-value",
				Path:     "/",
				Secure:   true,
				HttpOnly: true,
			})
			http.Redirect(w, r, "/", http.StatusFound)
		case "/api/auth/totp-challenge/verify":
			ts.totpCalls++
			ts.lastTOTPBody = decodeBody(t, r)
			http.SetCookie(w, &http.Cookie{
				Name:     "__Secure-next-auth.session-token",
				Value:    "session-token-value",
				Path:     "/",
				Secure:   true,
				HttpOnly: true,
			})
			fmt.Fprint(w, `{"redirect":"/?login-source=floatingSignup"}`)
		default:
			http.Error(w, "not found: "+r.URL.Path, http.StatusNotFound)
		}
	})

	ts.srv = httptest.NewServer(mux)
	t.Cleanup(ts.srv.Close)

	c, err := transport.NewPlain(transport.Options{
		BaseURL:    ts.srv.URL,
		APIVersion: "2.18",
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPlain: %v", err)
	}
	ts.client = &recordingClient{Client: c}
	return ts
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func (ts *authTestServer) spec() *spec.Spec {
	s := &spec.Spec{
		BaseURL:           ts.srv.URL,
		APIVersion:        "2.18",
		SessionCookieName: "__Secure-next-auth.session-token",
	}
	s.Endpoints.AuthCSRF = "/api/auth/csrf"
	s.Endpoints.AuthSigninEmail = "/api/auth/signin/email"
	s.Endpoints.AuthOTPRedirect = "/api/auth/otp-redirect-link"
	s.Endpoints.AuthTOTPVerify = "/api/auth/totp-challenge/verify"
	return s
}

func TestLogin_OTPFlow(t *testing.T) {
	ts := newAuthTestServer(t, false /* totpRequired */)
	auth := NewAuth(ts.client, ts.spec())
	ctx := context.Background()

	if err := auth.RequestCode(ctx, "user@example.com"); err != nil {
		t.Fatalf("RequestCode: %v", err)
	}
	if ts.csrfCalls != 1 {
		t.Errorf("csrf calls = %d, want 1", ts.csrfCalls)
	}
	if ts.signinCalls != 1 {
		t.Errorf("signin calls = %d, want 1", ts.signinCalls)
	}
	if ts.lastSigninBody["email"] != "user@example.com" {
		t.Errorf("signin email = %v", ts.lastSigninBody["email"])
	}
	if ts.lastSigninBody["csrfToken"] != "csrf-123" {
		t.Errorf("signin csrfToken = %v", ts.lastSigninBody["csrfToken"])
	}
	if ts.lastSigninBody["useNumericOtp"] != "true" {
		t.Errorf("useNumericOtp = %v", ts.lastSigninBody["useNumericOtp"])
	}

	token, err := auth.CompleteLogin(ctx, "user@example.com", "123456", "")
	if err != nil {
		t.Fatalf("CompleteLogin: %v", err)
	}
	if token != "session-token-value" {
		t.Fatalf("token = %q, want session-token-value", token)
	}
	if ts.otpRedirectCalls != 1 {
		t.Errorf("otp-redirect calls = %d, want 1", ts.otpRedirectCalls)
	}
	if ts.callbackCalls != 1 {
		t.Errorf("callback calls = %d, want 1", ts.callbackCalls)
	}
	if ts.totpCalls != 0 {
		t.Errorf("totp calls = %d, want 0 when not required", ts.totpCalls)
	}

	// Save to a token store and verify freshness + persisted expiry.
	dir := t.TempDir()
	store := NewTokenStore(filepath.Join(dir, "token.json"))
	tok, err := store.Save(token, DefaultTokenTTL)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !tok.Fresh(time.Now()) {
		t.Error("saved token should be fresh")
	}
	if got := tok.ExpiresAt.Sub(tok.ObtainedAt); got < 29*24*time.Hour || got > 31*24*time.Hour {
		t.Errorf("expiry window = %v, want ~30 days", got)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Value != "session-token-value" {
		t.Errorf("loaded.Value = %q", loaded.Value)
	}

	// Expiry check: an expired file must fail with ErrTokenExpired.
	expired := &Token{Value: "x", ObtainedAt: time.Now().Add(-31 * 24 * time.Hour), ExpiresAt: time.Now().Add(-24 * time.Hour)}
	data, _ := json.Marshal(expired)
	if err := os.WriteFile(store.path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != ErrTokenExpired {
		t.Errorf("Load expired = %v, want ErrTokenExpired", err)
	}
}

func TestLogin_TOTPRequired(t *testing.T) {
	ts := newAuthTestServer(t, true /* totpRequired */)
	auth := NewAuth(ts.client, ts.spec())
	ctx := context.Background()

	if err := auth.RequestCode(ctx, "user@example.com"); err != nil {
		t.Fatalf("RequestCode: %v", err)
	}

	// Without a TOTP code the flow reports the challenge instead of failing
	// opaquely.
	if _, err := auth.CompleteLogin(ctx, "user@example.com", "123456", ""); err != ErrTOTPRequired {
		t.Fatalf("CompleteLogin without totp: got %v, want ErrTOTPRequired", err)
	}
	if ts.totpCalls != 0 {
		t.Errorf("totp calls before code = %d, want 0", ts.totpCalls)
	}

	token, err := auth.CompleteLogin(ctx, "user@example.com", "123456", "987654")
	if err != nil {
		t.Fatalf("CompleteLogin with totp: %v", err)
	}
	if token != "session-token-value" {
		t.Fatalf("token = %q", token)
	}
	if ts.totpCalls != 1 {
		t.Errorf("totp calls = %d, want 1", ts.totpCalls)
	}
	// The OTP was consumed by the first callback; the retry must resume at
	// TOTP verification instead of re-exchanging the code.
	if ts.otpRedirectCalls != 1 {
		t.Errorf("otp-redirect calls = %d, want 1 (OTP must not be re-exchanged)", ts.otpRedirectCalls)
	}
	if ts.callbackCalls != 1 {
		t.Errorf("callback calls = %d, want 1", ts.callbackCalls)
	}
	if ts.lastTOTPBody["token"] != "challenge-tok" {
		t.Errorf("totp body token = %v", ts.lastTOTPBody["token"])
	}
	if ts.lastTOTPBody["code"] != "987654" {
		t.Errorf("totp body code = %v", ts.lastTOTPBody["code"])
	}
}

func TestLogin_BadCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, "<html>perplexity</html>")
		case "/api/auth/csrf":
			fmt.Fprint(w, `{"csrfToken":"csrf-123"}`)
		case "/api/auth/signin/email":
			fmt.Fprint(w, `{}`)
		case "/api/auth/otp-redirect-link":
			http.Error(w, "invalid code", http.StatusForbidden)
		default:
			http.Error(w, "nf", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := transport.NewPlain(transport.Options{BaseURL: srv.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sp := spec.Spec{BaseURL: srv.URL, APIVersion: "2.18"}
	sp.Endpoints.AuthCSRF = "/api/auth/csrf"
	sp.Endpoints.AuthSigninEmail = "/api/auth/signin/email"
	sp.Endpoints.AuthOTPRedirect = "/api/auth/otp-redirect-link"

	auth := NewAuth(c, &sp)
	ctx := context.Background()
	if err := auth.RequestCode(ctx, "user@example.com"); err != nil {
		t.Fatal(err)
	}
	_, err = auth.CompleteLogin(ctx, "user@example.com", "000000", "")
	if err == nil || !strings.Contains(err.Error(), "verify code") {
		t.Fatalf("err = %v, want verify code failure", err)
	}
}

func TestTokenStore_MissingAndEmpty(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), "nested", "token.json"))
	if _, err := store.Load(); err != ErrNoToken {
		t.Errorf("missing file: err = %v, want ErrNoToken", err)
	}
	if _, err := store.Save("", time.Hour); err == nil {
		t.Error("Save empty token should error")
	}
	// Save creates the missing parent directory.
	if _, err := store.Save("tok", time.Hour); err != nil {
		t.Fatalf("Save with nested dir: %v", err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load after save: %v", err)
	}
	// Empty value on disk -> ErrNoToken.
	os.WriteFile(store.path, []byte(`{"token":""}`), 0o600)
	if _, err := store.Load(); err != ErrNoToken {
		t.Errorf("empty token: err = %v, want ErrNoToken", err)
	}
}

func TestLogin_TOTPVerifyViaRedirect(t *testing.T) {
	// Reference allows the TOTP verify to answer 3xx + Location instead of
	// 200 JSON; the redirect must be followed to collect the session cookie.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/totp-challenge/verify":
			http.SetCookie(w, &http.Cookie{Name: "__Secure-next-auth.session-token", Value: "tok-3xx", Path: "/", Secure: true})
			w.Header().Set("Location", "/?login-source=floatingSignup")
			w.WriteHeader(http.StatusFound)
		case "/":
			fmt.Fprint(w, "<html></html>")
		default:
			http.Error(w, "nf", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := transport.NewPlain(transport.Options{BaseURL: srv.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	sp := spec.Spec{BaseURL: srv.URL, APIVersion: "2.18", SessionCookieName: "__Secure-next-auth.session-token"}
	sp.Endpoints.AuthTOTPVerify = "/api/auth/totp-challenge/verify"
	a := NewAuth(c, &sp)
	if err := a.verifyTOTP(context.Background(), "challenge-tok", "123456"); err != nil {
		t.Fatalf("verifyTOTP via 3xx: %v", err)
	}
	if got := c.Cookie("__Secure-next-auth.session-token"); got != "tok-3xx" {
		t.Fatalf("cookie = %q, want tok-3xx", got)
	}
}
