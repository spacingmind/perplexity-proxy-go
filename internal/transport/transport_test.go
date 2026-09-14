package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, baseURL string) Client {
	t.Helper()
	c, err := NewPlain(Options{
		BaseURL:    baseURL,
		APIVersion: "2.18",
		Timeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPlain: %v", err)
	}
	return c
}

func testOptions(baseURL string) Options {
	return Options{BaseURL: baseURL, APIVersion: "2.18"}
}

func TestInterfaceSatisfied(t *testing.T) {
	var _ Client = (*shared)(nil)
	if _, err := NewPlain(testOptions("http://example.com")); err != nil {
		t.Fatalf("NewPlain: %v", err)
	}
	// NewUTLS must at least construct without network access.
	if _, err := NewUTLS(testOptions("https://example.com")); err != nil {
		t.Fatalf("NewUTLS: %v", err)
	}
}

func TestHeaders_AppliedPerRequest(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var out map[string]any
	if err := c.PostJSON(ctx, "/rest/rate-limit/all", map[string]any{"a": 1}, &out); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}

	checks := map[string]string{
		"Accept":           "text/event-stream, application/json",
		"Content-Type":     "application/json",
		"x-app-apiclient":  "default",
		"x-app-apiversion": "2.18",
		"Referer":          srv.URL + "/",
		"Origin":           srv.URL,
	}
	for h, want := range checks {
		if got := got.Get(h); got != want {
			t.Errorf("header %s = %q, want %q", h, got, want)
		}
	}
	if ua := got.Get("User-Agent"); !strings.Contains(ua, "Mozilla/5.0") {
		t.Errorf("User-Agent = %q, want browser-like", ua)
	}
}

func TestHeaders_MinimalOnGet(t *testing.T) {
	var accept, ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept, ct = r.Header.Get("Accept"), r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), "/search/new?q=hi"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if accept != "" || ct != "" {
		t.Errorf("minimal GET headers leaked Accept=%q Content-Type=%q", accept, ct)
	}
}

func TestCookie_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		switch {
		case r.URL.Path == "/set":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "from-server", Path: "/"})
			fmt.Fprint(w, "ok")
		case err != nil:
			http.Error(w, "no cookie", http.StatusUnauthorized)
		default:
			fmt.Fprintf(w, `{"seen":"%s"}`, cookie.Value)
		}
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Server-set cookie persists in the jar and round-trips.
	if err := c.Get(ctx, "/set"); err != nil {
		t.Fatalf("Get /set: %v", err)
	}
	if got := c.Cookie("session"); got != "from-server" {
		t.Fatalf("Cookie after server set = %q, want from-server", got)
	}
	var out map[string]any
	if err := c.GetJSON(ctx, "/echo", &out); err != nil {
		t.Fatalf("GetJSON /echo: %v", err)
	}
	if out["seen"] != "from-server" {
		t.Fatalf("server saw cookie %v, want from-server", out["seen"])
	}

	// Client-set cookie replaces it.
	c.SetCookie("session", "manual")
	if err := c.GetJSON(ctx, "/echo", &out); err != nil {
		t.Fatalf("GetJSON /echo: %v", err)
	}
	if out["seen"] != "manual" {
		t.Fatalf("server saw cookie %v, want manual", out["seen"])
	}
	if got := c.Cookie("session"); got != "manual" {
		t.Fatalf("Cookie = %q, want manual", got)
	}
}

func TestCookie_ChunkedReassembly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session.0", Value: "AAA", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "session.1", Value: "BBB", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "session.10", Value: "CCC", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "session.2", Value: "DDD", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "session.-1", Value: "EVIL", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "session.+3", Value: "EVIL", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "session.x", Value: "EVIL", Path: "/"})
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), "/chunks"); err != nil {
		t.Fatal(err)
	}
	// numeric order: 0,1,2,10 — not lexicographic
	if got := c.Cookie("session"); got != "AAABBBDDDCCC" {
		t.Fatalf("chunked cookie = %q, want AAABBBDDDCCC", got)
	}
}

func TestPostSSE_LinesAndAbort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush := w.(http.Flusher)
		for _, line := range []string{"data: one", "data: two", "", "data: three"} {
			fmt.Fprintln(w, line)
			flush.Flush()
		}
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var lines []string
	err = c.PostSSE(ctx, "/rest/sse/perplexity_ask", map[string]any{}, func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("PostSSE: %v", err)
	}
	if len(lines) != 3 || lines[0] != "data: one" || lines[2] != "data: three" {
		t.Fatalf("lines = %v", lines)
	}

	// onLine error aborts and propagates.
	sentinel := fmt.Errorf("stop")
	err = c.PostSSE(ctx, "/rest/sse/perplexity_ask", map[string]any{}, func(line []byte) error {
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("PostSSE abort: got %v, want sentinel", err)
	}
}

func TestPostSSE_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	err = c.PostSSE(context.Background(), "/rest/sse/perplexity_ask", map[string]any{}, func([]byte) error { return nil })
	var se *StatusError
	if !errorsAs(err, &se) || se.StatusCode != 403 {
		t.Fatalf("err = %v, want StatusError 403", err)
	}
}

func TestGetNoRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/auth/callback?ok=1", http.StatusFound)
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	status, loc, err := c.GetNoRedirect(context.Background(), "/login")
	if err != nil {
		t.Fatalf("GetNoRedirect: %v", err)
	}
	if status != 302 {
		t.Fatalf("status = %d, want 302", status)
	}
	if !strings.Contains(loc, "/api/auth/callback") {
		t.Fatalf("location = %q", loc)
	}
}

func TestAbsoluteURLPassThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"where":"remote"}`)
	}))
	defer srv.Close()

	// base points elsewhere; absolute URL wins
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"where":"other"}`)
	}))
	defer other.Close()

	c, err := NewPlain(testOptions(other.URL))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := c.GetJSON(context.Background(), srv.URL+"/x", &out); err != nil {
		t.Fatal(err)
	}
	if out["where"] != "remote" {
		t.Fatalf("where = %v, want remote (absolute URL should pass through)", out["where"])
	}
}

func TestJSONRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		json.Unmarshal(body, &in)
		in["echo"] = true
		json.NewEncoder(w).Encode(in)
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := c.PostJSON(context.Background(), "/p", map[string]any{"q": "hello"}, &out); err != nil {
		t.Fatal(err)
	}
	if out["q"] != "hello" || out["echo"] != true {
		t.Fatalf("out = %v", out)
	}
}

func TestScanLines_TrailingNoNewline(t *testing.T) {
	var lines []string
	if err := scanLines(strings.NewReader("a\nb\nc"), func(l []byte) error {
		lines = append(lines, string(l))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || lines[2] != "c" {
		t.Fatalf("lines = %v", lines)
	}
}

func errorsAs(err error, target *(*StatusError)) bool {
	se, ok := err.(*StatusError)
	if ok {
		*target = se
	}
	return ok
}

func TestCookieJar_HostScoped(t *testing.T) {
	var leaked string
	// "localhost" and "127.0.0.1" are distinct hostnames for cookie scoping
	// (cookies are port-agnostic per RFC 6265, so two ports alone won't do).
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err == nil {
			leaked = c.Value
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer evil.Close()
	evilHost := "localhost" + evil.URL[strings.LastIndex(evil.URL, ":"):]

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+evilHost+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	c.SetCookie("session", "secret-token")

	var out map[string]any
	if err := c.GetJSON(context.Background(), "/redirect", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if leaked != "" {
		t.Fatalf("session cookie leaked to %s", evilHost)
	}

	// Absolute-URL request off-host must not carry it either.
	if err := c.GetJSON(context.Background(), "http://"+evilHost+"/direct", &out); err != nil {
		t.Fatalf("GetJSON absolute: %v", err)
	}
	if leaked != "" {
		t.Fatal("session cookie leaked via absolute URL request")
	}
}

func TestCookie_ChunkDeletionMidSession(t *testing.T) {
	// Server sets chunked cookie, then deletes chunk 1 (MaxAge<0);
	// reassembly must reflect the deletion, not resurrect stale chunks.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/set":
			http.SetCookie(w, &http.Cookie{Name: "session.0", Value: "AAA"})
			http.SetCookie(w, &http.Cookie{Name: "session.1", Value: "BBB"})
		case "/delete":
			http.SetCookie(w, &http.Cookie{Name: "session.1", Value: "", MaxAge: -1})
			http.SetCookie(w, &http.Cookie{Name: "session.0", Value: "AAA"})
		}
	}))
	defer srv.Close()

	c, err := NewPlain(testOptions(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Get(ctx, "/set"); err != nil {
		t.Fatal(err)
	}
	if got := c.Cookie("session"); got != "AAABBB" {
		t.Fatalf("before delete = %q, want AAABBB", got)
	}
	if err := c.Get(ctx, "/delete"); err != nil {
		t.Fatal(err)
	}
	if got := c.Cookie("session"); got != "AAA" {
		t.Fatalf("after delete = %q, want AAA (chunk 1 removed)", got)
	}
}
