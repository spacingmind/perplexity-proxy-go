package main

import (
	"bytes"
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

	"github.com/spacingmind/perplexity-proxy-go/internal/pplx"
	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// cliTest wires the CLI seams (transport factory, stdin, token path) to an
// httptest-backed Perplexity mock.
type cliTest struct {
	server *httptest.Server
	out    bytes.Buffer
}

func newCLITest(t *testing.T, askSSE string) *cliTest {
	t.Helper()
	ct := &cliTest{}

	mux := http.NewServeMux()
	mux.HandleFunc("/rest/rate-limit/all", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"remaining_pro":3,"remaining_research":1,"remaining_labs":5,"remaining_agentic_research":2,
			"sources":{"source_to_limit":{"web":{"monthly_limit":null},"scholar":{"monthly_limit":50,"remaining":12}}}}`)
	})
	mux.HandleFunc("/search/new", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/rest/sse/perplexity_ask", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, askSSE)
		w.(http.Flusher).Flush()
	})
	ct.server = httptest.NewServer(mux)
	t.Cleanup(ct.server.Close)

	origTransport := newTransport
	newTransport = func(sp *spec.Spec) (transport.Client, error) {
		return transport.NewPlain(transport.Options{BaseURL: ct.server.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
	}
	t.Cleanup(func() { newTransport = origTransport })

	// Env-level spec override so Load() sees the test server.
	t.Setenv("PPLX_BASE_URL", ct.server.URL)

	// Token file seam: point the store at a temp file and pre-save a token.
	t.Setenv("PPLX_TOKEN_PATH", filepath.Join(t.TempDir(), "token.json"))

	origStdout := stdout
	stdout = &ct.out
	t.Cleanup(func() { stdout = origStdout })

	return ct
}

const cliSSE = "data: {\"backend_uuid\":\"u1\",\"thread_title\":\"T\"}\n" +
	"data: {\"text\":\"{\\\"answer\\\":\\\"Paris [1] is the capital.\\\",\\\"web_results\\\":[{\\\"name\\\":\\\"Wiki\\\",\\\"url\\\":\\\"https://w.example\\\"},{\\\"name\\\":\\\"Brit\\\",\\\"url\\\":\\\"https://b.example\\\"}]}\"}\n" +
	"data: {\"final\":true}\n"

func TestCLI_AskPrintsCitations(t *testing.T) {
	ct := newCLITest(t, cliSSE)
	if err := saveTestToken(t); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"ask", "what is the capital of france", "-m", "best"})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	out := ct.output(t)

	if !strings.Contains(out, "Paris [1] is the capital.") {
		t.Errorf("answer missing in output:\n%s", out)
	}
	if !strings.Contains(out, "Sources:") {
		t.Errorf("Sources header missing:\n%s", out)
	}
	if !strings.Contains(out, "[1] Wiki — https://w.example") {
		t.Errorf("citation 1 missing:\n%s", out)
	}
	if !strings.Contains(out, "[2] Brit — https://b.example") {
		t.Errorf("citation 2 missing:\n%s", out)
	}
}

func TestCLI_AskNoCitations(t *testing.T) {
	ct := newCLITest(t, cliSSE)
	if err := saveTestToken(t); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"ask", "q", "--no-citations"}); err != nil {
		t.Fatal(err)
	}
	out := ct.output(t)
	if strings.Contains(out, "Sources:") {
		t.Errorf("--no-citations should suppress sources:\n%s", out)
	}
	if !strings.Contains(out, "Paris") {
		t.Errorf("answer missing:\n%s", out)
	}
}

func TestCLI_Usage(t *testing.T) {
	ct := newCLITest(t, "")
	if err := saveTestToken(t); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"usage"}); err != nil {
		t.Fatalf("usage: %v", err)
	}
	out := ct.output(t)
	for _, want := range []string{"Pro Search:          3", "Deep Research:       1", "Create Files & Apps: 5", "Browser Agent:       2", "source scholar: 12/50"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "source web:") {
		t.Errorf("unlimited web source should not be listed:\n%s", out)
	}
}

func TestCLI_AskWithoutToken(t *testing.T) {
	newCLITest(t, cliSSE) // no token saved
	err := run([]string{"ask", "q"})
	if err == nil || !strings.Contains(err.Error(), "pplx login") {
		t.Fatalf("err = %v, want login hint", err)
	}
}

func TestCLI_LoginFlow(t *testing.T) {
	ct := &cliTest{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, "<html></html>")
		case "/api/auth/csrf":
			fmt.Fprint(w, `{"csrfToken":"c1"}`)
		case "/api/auth/signin/email":
			fmt.Fprint(w, `{}`)
		case "/api/auth/otp-redirect-link":
			fmt.Fprint(w, `{"redirect":"/api/auth/callback/x"}`)
		case "/api/auth/callback/x":
			http.SetCookie(w, &http.Cookie{Name: "__Secure-next-auth.session-token", Value: "tok-cli", Path: "/", Secure: true})
			http.Redirect(w, r, "/", http.StatusFound)
		default:
			if r.URL.Path != "/api/auth/totp-challenge/verify" {
				http.Error(w, "nf", 404)
			}
		}
	})
	ct.server = httptest.NewServer(mux)
	t.Cleanup(ct.server.Close)

	origTransport := newTransport
	newTransport = func(sp *spec.Spec) (transport.Client, error) {
		return transport.NewPlain(transport.Options{BaseURL: ct.server.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
	}
	t.Cleanup(func() { origTransportF(origTransport) })
	t.Setenv("PPLX_BASE_URL", ct.server.URL)
	tokenPath := filepath.Join(t.TempDir(), "token.json")
	t.Setenv("PPLX_TOKEN_PATH", tokenPath)

	stdin = strings.NewReader("user@example.com\n654321\n")
	t.Cleanup(func() { stdin = os.Stdin })

	if err := run([]string{"login", "-email", "user@example.com"}); err != nil {
		t.Fatalf("login: %v", err)
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	var tok pplx.Token
	if err := json.Unmarshal(data, &tok); err != nil {
		t.Fatal(err)
	}
	if tok.Value != "tok-cli" {
		t.Errorf("token = %q", tok.Value)
	}
	if !tok.Fresh(time.Now()) {
		t.Error("token should be fresh")
	}
}

func origTransportF(f func(*spec.Spec) (transport.Client, error)) { newTransport = f }

func saveTestToken(t *testing.T) error {
	t.Helper()
	path := os.Getenv("PPLX_TOKEN_PATH")
	store := pplx.NewTokenStore(path)
	_, err := store.Save("test-token", time.Hour)
	return err
}

func TestCLI_FriendlyAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	orig := newTransport
	newTransport = func(sp *spec.Spec) (transport.Client, error) {
		return transport.NewPlain(transport.Options{BaseURL: srv.URL, APIVersion: "2.18", Timeout: 5 * time.Second})
	}
	t.Cleanup(func() { newTransport = orig })
	t.Setenv("PPLX_BASE_URL", srv.URL)
	t.Setenv("PPLX_TOKEN_PATH", filepath.Join(t.TempDir(), "token.json"))
	if err := saveTestToken(t); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"usage"})
	if err == nil || !strings.Contains(err.Error(), "pplx login") {
		t.Fatalf("err = %v, want friendly 403 message", err)
	}
}

func TestCLI_PrintAnswerNil(t *testing.T) {
	var b bytes.Buffer
	printAnswer(&b, nil, true) // must not panic
	printAnswer(&b, &pplx.Answer{}, true)
	if !strings.Contains(b.String(), "(no answer returned)") {
		t.Errorf("out = %q", b.String())
	}
}

// output captures the test's stdout target.
func (ct *cliTest) output(t *testing.T) string {
	t.Helper()
	return ct.out.String()
}

var _ = context.Background
