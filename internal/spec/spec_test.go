package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func withTestOverrides(t *testing.T, fileContent string, env map[string]string) *Spec {
	t.Helper()
	dir := t.TempDir()
	origFile := OverridesFile
	if fileContent == "" {
		OverridesFile = filepath.Join(dir, "spec-overrides.json") // does not exist
	} else {
		path := filepath.Join(dir, "spec-overrides.json")
		if err := os.WriteFile(path, []byte(fileContent), 0o600); err != nil {
			t.Fatal(err)
		}
		OverridesFile = path
	}
	t.Cleanup(func() { OverridesFile = origFile })

	for k, v := range env {
		t.Setenv(k, v)
	}
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestSpec_EmbeddedValues(t *testing.T) {
	s := withTestOverrides(t, "", nil)
	if s.BaseURL != "https://www.perplexity.ai" {
		t.Errorf("BaseURL = %q", s.BaseURL)
	}
	if s.APIVersion != "2.18" {
		t.Errorf("APIVersion = %q", s.APIVersion)
	}
	if s.SessionCookieName != "__Secure-next-auth.session-token" {
		t.Errorf("SessionCookieName = %q", s.SessionCookieName)
	}
	if s.Endpoints.Ask != "/rest/sse/perplexity_ask" {
		t.Errorf("Endpoints.Ask = %q", s.Endpoints.Ask)
	}
	if s.Endpoints.RateLimits != "/rest/rate-limit/all" {
		t.Errorf("Endpoints.RateLimits = %q", s.Endpoints.RateLimits)
	}
	if len(s.Models) == 0 {
		t.Error("no models embedded")
	}
}

func TestSpec_OverridesPrecedence(t *testing.T) {
	// embedded < file < env: file sets api_version 2.19 and a base_url,
	// env overrides base_url again and bumps api_version to 2.20.
	file := `{
		"api_version": "2.19",
		"base_url": "https://file.example.com",
		"endpoints": {"ask": "/rest/sse/file_ask"}
	}`
	s := withTestOverrides(t, file, map[string]string{
		"PPLX_BASE_URL":     "https://env.example.com",
		"PPLX_API_VERSION":  "2.20",
		"PPLX_ENDPOINT_ASK": "/rest/sse/env_ask",
	})

	if got := s.BaseURL; got != "https://env.example.com" {
		t.Errorf("base_url: env should win, got %q", got)
	}
	if got := s.APIVersion; got != "2.20" {
		t.Errorf("api_version: env should win, got %q", got)
	}
	if got := s.Endpoints.Ask; got != "/rest/sse/env_ask" {
		t.Errorf("endpoints.ask: env should win, got %q", got)
	}
	// file override without env wins over embedded
	if got := s.Endpoints.SearchInit; got != "/search/new" {
		t.Errorf("endpoints.search_init should stay embedded, got %q", got)
	}

	// file-only fields (no env counterpart) must survive
	s2 := withTestOverrides(t, file, map[string]string{
		"PPLX_BASE_URL":     "",
		"PPLX_API_VERSION":  "",
		"PPLX_ENDPOINT_ASK": "",
	})
	if got := s2.APIVersion; got != "2.19" {
		t.Errorf("api_version: file should beat embedded, got %q", got)
	}
	if got := s2.BaseURL; got != "https://file.example.com" {
		t.Errorf("base_url: file should beat embedded, got %q", got)
	}
	if got := s2.Endpoints.Ask; got != "/rest/sse/file_ask" {
		t.Errorf("endpoints.ask: file should beat embedded, got %q", got)
	}
}

func TestSpec_OverridesFileIgnoredWhenMissing(t *testing.T) {
	s := withTestOverrides(t, "", map[string]string{"PPLX_API_VERSION": "9.9"})
	if s.APIVersion != "9.9" {
		t.Errorf("APIVersion = %q, want env value 9.9", s.APIVersion)
	}
	if s.BaseURL != "https://www.perplexity.ai" {
		t.Errorf("BaseURL = %q, want embedded", s.BaseURL)
	}
}

func TestSpec_OverridesFileMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec-overrides.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := OverridesFile
	OverridesFile = path
	t.Cleanup(func() { OverridesFile = orig })
	if _, err := Load(); err == nil {
		t.Error("expected error for malformed overrides file")
	}
}

func TestSpec_ModelLookup(t *testing.T) {
	s := withTestOverrides(t, "", nil)
	m := s.Model("glm")
	if m.Identifier != "glm_5_2" {
		t.Errorf("Model(glm).Identifier = %q", m.Identifier)
	}
	if m := s.Model("experimental"); m.Mode != "concise" {
		t.Errorf("Model(experimental).Mode = %q, want concise (auto was dropped)", m.Mode)
	}
	// lookup by identifier
	if m := s.Model("glm_5_2"); m.Name != "glm" {
		t.Errorf("Model(glm_5_2).Name = %q", m.Name)
	}
	// unknown and empty fall back to best (the new default)
	if m := s.Model("nope"); m.Identifier != "pplx_pro" {
		t.Errorf("Model(nope).Identifier = %q, want pplx_pro fallback", m.Identifier)
	}
	if m := s.Model(""); m.Identifier != "pplx_pro" {
		t.Errorf(`Model("").Identifier = %q, want pplx_pro default`, m.Identifier)
	}
}

func TestSpec_RawIsEmbeddedJSON(t *testing.T) {
	s := withTestOverrides(t, "", nil)
	var probe struct {
		APIVersion string `json:"api_version"`
	}
	if err := json.Unmarshal(s.Raw, &probe); err != nil {
		t.Fatalf("Raw not valid JSON: %v", err)
	}
	if probe.APIVersion != s.APIVersion {
		t.Errorf("Raw api_version %q != effective %q", probe.APIVersion, s.APIVersion)
	}
}
