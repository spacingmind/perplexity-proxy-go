package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
)

const upstreamConstants = `"""Constants and values for the Perplexity internal API."""

from __future__ import annotations

from re import Pattern, compile
from typing import Final


API_VERSION: Final[str] = "2.19"
"""Current API version used by Perplexity WebUI."""

APP_HEADERS: Final[dict[str, str]] = {
    "x-app-apiclient": "default",
    "x-app-apiversion": API_VERSION,
}

API_BASE_URL: Final[str] = "https://www.perplexity.ai"

ENDPOINT_ASK: Final[str] = "/rest/sse/perplexity_ask"
ENDPOINT_SEARCH_INIT: Final[str] = "/search/new/v2"
ENDPOINT_RATE_LIMITS: Final[str] = "/rest/rate-limit/all"

SESSION_COOKIE_NAME: Final[str] = "__Secure-next-auth.session-token"
`

const upstreamModels = `from dataclasses import dataclass

@dataclass(frozen=True, slots=True)
class Model:
    identifier: str
    mode: str = "copilot"

class Models:
    AUTO = Model(identifier="auto", mode="concise")
    BEST = Model(identifier="pplx_pro")
    GLM_5_2 = Model(identifier="glm_5_2")
    KIMI_K2_6 = Model(identifier="kimik26instant")
    BRAND_NEW = Model(identifier="newmodel42")
`

func newSyncTest(t *testing.T) (srvURL string, overridesPath string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimSuffix(r.URL.Path, "/") {
		case "/src/perplexity_web_mcp/constants.py":
			fmt.Fprint(w, upstreamConstants)
		case "/src/perplexity_web_mcp/models.py":
			fmt.Fprint(w, upstreamModels)
		default:
			http.Error(w, "nf", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	origURLs := syncSourceURLs
	syncSourceURLs = func() map[string]string {
		return map[string]string{
			"constants": srv.URL + "/src/perplexity_web_mcp/constants.py",
			"models":    srv.URL + "/src/perplexity_web_mcp/models.py",
		}
	}
	t.Cleanup(func() { syncSourceURLs = origURLs })

	overridesPath = filepath.Join(t.TempDir(), "spec-overrides.json")
	origOverrides := spec.OverridesFile
	spec.OverridesFile = overridesPath
	t.Cleanup(func() { spec.OverridesFile = origOverrides })

	return srv.URL, overridesPath
}

func TestSpecSync_DetectsVersionBump(t *testing.T) {
	_, overridesPath := newSyncTest(t)

	// --yes writes without prompting
	out, err := runCapture(t, []string{"spec", "sync", "--yes"})
	if err != nil {
		t.Fatalf("spec sync --yes: %v", err)
	}

	if !strings.Contains(out, `api_version: "2.18" -> "2.19"`) {
		t.Errorf("diff must mention version bump:\n%s", out)
	}
	if !strings.Contains(out, `endpoints.search_init: "/search/new" -> "/search/new/v2"`) {
		t.Errorf("diff must mention moved endpoint:\n%s", out)
	}
	if !strings.Contains(out, `newmodel42`) {
		t.Errorf("diff must mention new model identifier:\n%s", out)
	}

	data, err := os.ReadFile(overridesPath)
	if err != nil {
		t.Fatalf("overrides not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"api_version": "2.19"`) {
		t.Errorf("overrides file missing api_version 2.19:\n%s", content)
	}
	if !strings.Contains(content, `"search_init": "/search/new/v2"`) {
		t.Errorf("overrides file missing search_init:\n%s", content)
	}
	// unchanged endpoints must not be persisted
	if strings.Contains(content, "rate-limit") {
		t.Errorf("overrides file should only carry changed keys:\n%s", content)
	}

	// effective spec now reflects the bump
	sp, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	if sp.APIVersion != "2.19" {
		t.Errorf("effective api_version = %q, want 2.19", sp.APIVersion)
	}
	if sp.Endpoints.SearchInit != "/search/new/v2" {
		t.Errorf("effective search_init = %q", sp.Endpoints.SearchInit)
	}
}

func TestSpecSync_DeclineDoesNotWrite(t *testing.T) {
	_, overridesPath := newSyncTest(t)

	stdin = strings.NewReader("n\n")
	t.Cleanup(func() { stdin = os.Stdin })

	out, err := runCapture(t, []string{"spec", "sync"})
	if err != nil {
		t.Fatalf("spec sync: %v", err)
	}
	if !strings.Contains(out, "Declined") {
		t.Errorf("expected decline notice:\n%s", out)
	}
	if _, err := os.Stat(overridesPath); !os.IsNotExist(err) {
		t.Errorf("overrides file must not be written on decline: %v", err)
	}
}

func TestSpecSync_NoDrift(t *testing.T) {
	// Serve the embedded values verbatim: no diff -> no write.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "constants.py") {
			fmt.Fprint(w, `API_VERSION: Final[str] = "2.18"
API_BASE_URL: Final[str] = "https://www.perplexity.ai"
ENDPOINT_ASK: Final[str] = "/rest/sse/perplexity_ask"
ENDPOINT_SEARCH_INIT: Final[str] = "/search/new"
SESSION_COOKIE_NAME: Final[str] = "__Secure-next-auth.session-token"
`)
			return
		}
		fmt.Fprint(w, `AUTO = Model(identifier="auto", mode="concise")
BEST = Model(identifier="pplx_pro")
`)
	}))
	defer srv.Close()

	origURLs := syncSourceURLs
	syncSourceURLs = func() map[string]string {
		return map[string]string{
			"constants": srv.URL + "/c.py",
			"models":    srv.URL + "/m.py",
		}
	}
	t.Cleanup(func() { syncSourceURLs = origURLs })

	overridesPath := filepath.Join(t.TempDir(), "spec-overrides.json")
	orig := spec.OverridesFile
	spec.OverridesFile = overridesPath
	t.Cleanup(func() { spec.OverridesFile = orig })

	out, err := runCapture(t, []string{"spec", "sync", "--yes"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "in sync") {
		t.Errorf("expected in-sync notice:\n%s", out)
	}
	if _, err := os.Stat(overridesPath); !os.IsNotExist(err) {
		t.Error("overrides must not be written when in sync")
	}
}

func TestSpecShow(t *testing.T) {
	orig := spec.OverridesFile
	spec.OverridesFile = filepath.Join(t.TempDir(), "none.json")
	t.Cleanup(func() { spec.OverridesFile = orig })
	t.Setenv("PPLX_API_VERSION", "9.9")

	out, err := runCapture(t, []string{"spec", "show"})
	if err != nil {
		t.Fatalf("spec show: %v", err)
	}
	if !strings.Contains(out, "PPLX_API_VERSION") {
		t.Errorf("header should note env override:\n%s", out)
	}
	if !strings.Contains(out, `"api_version": "9.9"`) {
		t.Errorf("effective spec should carry env override:\n%s", out)
	}
}

func TestSpecCheck_DryRunNoNetwork(t *testing.T) {
	out, err := runCapture(t, []string{"spec", "check"})
	if err != nil {
		t.Fatalf("spec check: %v", err)
	}
	for _, want := range []string{"rate-limit/all", "perplexity_ask", "list_ask_threads", "No network calls"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// runCapture runs args with stdout captured and returns the output.
func runCapture(t *testing.T, args []string) (string, error) {
	t.Helper()
	orig := stdout
	var b strings.Builder
	stdout = &b
	t.Cleanup(func() { stdout = orig })
	err := run(args)
	return b.String(), err
}

func TestSpecShow_ReportsEndpointOverride(t *testing.T) {
	// A partial override touching only an endpoint (the shape `spec sync`
	// writes) must be reflected in the header, not reported as "none".
	orig := spec.OverridesFile
	spec.OverridesFile = filepath.Join(t.TempDir(), "none.json")
	t.Cleanup(func() { spec.OverridesFile = orig })

	sp, err := spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(overrideSources(sp), ", "), "none") {
		t.Fatal("precondition: no overrides expected")
	}

	path := filepath.Join(t.TempDir(), "ov.json")
	if err := os.WriteFile(path, []byte(`{"endpoints":{"search_init":"/search/new/v2"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec.OverridesFile = path

	sp, err = spec.Load()
	if err != nil {
		t.Fatal(err)
	}
	srcs := strings.Join(overrideSources(sp), ", ")
	if !strings.Contains(srcs, "endpoints.search_init") {
		t.Errorf("override sources = %q, want endpoints.search_init listed", srcs)
	}
	if strings.Contains(srcs, "none (embedded spec)") {
		t.Errorf("override sources = %q, must not claim none", srcs)
	}
}
