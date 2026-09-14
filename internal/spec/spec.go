// Package spec holds the Perplexity protocol constants as embedded data.
// All protocol knowledge (endpoints, API version, headers, models) lives here;
// the rest of the codebase reads it via Load and never hardcodes values.
package spec

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Spec is the effective protocol configuration after overrides are applied.
type Spec struct {
	BaseURL           string          `json:"base_url"`
	APIVersion        string          `json:"api_version"`
	SessionCookieName string          `json:"session_cookie_name"`
	Endpoints         Endpoints       `json:"endpoints"`
	Headers           Headers         `json:"headers"`
	Defaults          Defaults        `json:"defaults"`
	Models            []Model         `json:"models"`
	Raw               json.RawMessage `json:"-"`
}

type Endpoints struct {
	Ask             string `json:"ask"`
	SearchInit      string `json:"search_init"`
	Upload          string `json:"upload"`
	RateLimits      string `json:"rate_limits"`
	UserSettings    string `json:"user_settings"`
	ListThreads     string `json:"list_threads"`
	ThreadDetail    string `json:"thread_detail"`
	Credits         string `json:"credits"`
	AuthCSRF        string `json:"auth_csrf"`
	AuthOTPRedirect string `json:"auth_otp_redirect"`
	AuthSigninEmail string `json:"auth_signin_email"`
	AuthTOTPVerify  string `json:"auth_totp_verify"`
}

type Headers struct {
	AppAPIClient string `json:"x-app-apiclient"`
	Accept       string `json:"accept"`
	ContentType  string `json:"content_type"`
}

type Defaults struct {
	PromptSource            string `json:"prompt_source"`
	SendBackTextInStreaming bool   `json:"send_back_text_in_streaming_api"`
	UseSchematizedAPI       bool   `json:"use_schematized_api"`
	Language                string `json:"language"`
	SourceFocus             string `json:"source_focus"`
	SearchFocus             string `json:"search_focus"`
	SearchRecencyFilter     string `json:"search_recency_filter"`
	SaveToLibrary           bool   `json:"save_to_library"`
}

type Model struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Mode       string `json:"mode"`
}

//go:embed data/spec.json
var embedded []byte

// OverridesFile is the user-level override path, ~/.pplx/spec-overrides.json.
// Overridable when set (tests point it at a temp file).
var OverridesFile = filepath.Join(homeDir(), ".pplx", "spec-overrides.json")

// Load returns the effective spec: embedded values, then overrides file,
// then environment variables (highest precedence).
func Load() (*Spec, error) {
	s, err := parseEmbedded()
	if err != nil {
		return nil, err
	}
	if err := s.applyFileOverrides(); err != nil {
		return nil, err
	}
	s.applyEnvOverrides()
	return s, nil
}

func parseEmbedded() (*Spec, error) {
	var s Spec
	if err := json.Unmarshal(embedded, &s); err != nil {
		return nil, fmt.Errorf("parse embedded spec: %w", err)
	}
	s.Raw = json.RawMessage(embedded)
	return &s, nil
}

// applyFileOverrides patches the spec from OverridesFile when it exists.
// Unknown fields in the file are ignored so partial overrides work.
func (s *Spec) applyFileOverrides() error {
	data, err := os.ReadFile(OverridesFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read spec overrides: %w", err)
	}
	var ov struct {
		BaseURL           *string            `json:"base_url"`
		APIVersion        *string            `json:"api_version"`
		SessionCookieName *string            `json:"session_cookie_name"`
		Endpoints         map[string]*string `json:"endpoints"`
	}
	if err := json.Unmarshal(data, &ov); err != nil {
		return fmt.Errorf("parse spec overrides %s: %w", OverridesFile, err)
	}
	if ov.BaseURL != nil {
		s.BaseURL = *ov.BaseURL
	}
	if ov.APIVersion != nil {
		s.APIVersion = *ov.APIVersion
	}
	if ov.SessionCookieName != nil {
		s.SessionCookieName = *ov.SessionCookieName
	}
	for key, val := range ov.Endpoints {
		if val == nil {
			continue
		}
		s.Endpoints.set(key, *val)
	}
	return nil
}

func (e *Endpoints) set(key, val string) {
	switch key {
	case "ask":
		e.Ask = val
	case "search_init":
		e.SearchInit = val
	case "upload":
		e.Upload = val
	case "rate_limits":
		e.RateLimits = val
	case "user_settings":
		e.UserSettings = val
	case "list_threads":
		e.ListThreads = val
	case "thread_detail":
		e.ThreadDetail = val
	case "credits":
		e.Credits = val
	case "auth_csrf":
		e.AuthCSRF = val
	case "auth_otp_redirect":
		e.AuthOTPRedirect = val
	case "auth_signin_email":
		e.AuthSigninEmail = val
	case "auth_totp_verify":
		e.AuthTOTPVerify = val
	}
}

// applyEnvOverrides applies PPLX_* environment variables (highest precedence).
func (s *Spec) applyEnvOverrides() {
	if v := os.Getenv("PPLX_BASE_URL"); v != "" {
		s.BaseURL = v
	}
	if v := os.Getenv("PPLX_API_VERSION"); v != "" {
		s.APIVersion = v
	}
	if v := os.Getenv("PPLX_SESSION_COOKIE_NAME"); v != "" {
		s.SessionCookieName = v
	}
	if v := os.Getenv("PPLX_ENDPOINT_ASK"); v != "" {
		s.Endpoints.Ask = v
	}
}

// Model looks up a model by name or identifier. The empty string and
// unknown names fall back to "best" (matches the Python default).
func (s *Spec) Model(name string) Model {
	for _, m := range s.Models {
		if m.Name == name || m.Identifier == name {
			return m
		}
	}
	for _, m := range s.Models {
		if m.Name == "best" {
			return m
		}
	}
	return Model{}
}

// SyncSourceURLs lists the upstream files `pplx spec sync` fetches to detect
// protocol drift, keyed by purpose.
func SyncSourceURLs() map[string]string {
	return map[string]string{
		"constants": "https://raw.githubusercontent.com/jacob-bd/perplexity-web-mcp/main/src/perplexity_web_mcp/constants.py",
		"models":    "https://raw.githubusercontent.com/jacob-bd/perplexity-web-mcp/main/src/perplexity_web_mcp/models.py",
	}
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "."
}

// Get returns the endpoint path for a spec key, or "" when unknown.
func (e Endpoints) Get(key string) string {
	switch key {
	case "ask":
		return e.Ask
	case "search_init":
		return e.SearchInit
	case "upload":
		return e.Upload
	case "rate_limits":
		return e.RateLimits
	case "user_settings":
		return e.UserSettings
	case "list_threads":
		return e.ListThreads
	case "thread_detail":
		return e.ThreadDetail
	case "credits":
		return e.Credits
	case "auth_csrf":
		return e.AuthCSRF
	case "auth_otp_redirect":
		return e.AuthOTPRedirect
	case "auth_signin_email":
		return e.AuthSigninEmail
	case "auth_totp_verify":
		return e.AuthTOTPVerify
	}
	return ""
}
