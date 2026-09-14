package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spacingmind/perplexity-proxy-go/internal/pplx"
	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// syncSourceURLs is the seam for the upstream spec sources; tests point it
// at an httptest server.
var syncSourceURLs = spec.SyncSourceURLs

// fetchUpstream GETs a sync source (production: raw.githubusercontent.com).
var fetchUpstream = func(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func cmdSpec(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pplx spec show|sync|check")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "show":
		return cmdSpecShow(rest)
	case "sync":
		return cmdSpecSync(rest)
	case "check":
		return cmdSpecCheck(rest)
	default:
		return fmt.Errorf("unknown spec subcommand %q (want show, sync, or check)", sub)
	}
}

func cmdSpecShow(args []string) error {
	if len(args) > 0 {
		return errors.New("usage: pplx spec show")
	}
	sp, err := spec.Load()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "# overrides applied: %s\n", strings.Join(overrideSources(sp), ", "))
	data, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(data))
	return nil
}

// overrideSources reports which override layers actually changed the
// effective spec, for the `spec show` header.
func overrideSources(eff *spec.Spec) []string {
	var out []string
	var base spec.Spec
	if err := json.Unmarshal(eff.Raw, &base); err == nil {
		var keys []string
		if base.APIVersion != eff.APIVersion {
			keys = append(keys, "api_version")
		}
		if base.BaseURL != eff.BaseURL {
			keys = append(keys, "base_url")
		}
		if base.SessionCookieName != eff.SessionCookieName {
			keys = append(keys, "session_cookie_name")
		}
		for _, k := range []string{
			"ask", "search_init", "upload", "rate_limits", "user_settings",
			"list_threads", "thread_detail", "credits", "auth_csrf",
			"auth_otp_redirect", "auth_signin_email", "auth_totp_verify",
		} {
			if base.Endpoints.Get(k) != eff.Endpoints.Get(k) {
				keys = append(keys, "endpoints."+k)
			}
		}
		if keys != nil {
			out = append(out, fmt.Sprintf("file/env (%s)", strings.Join(keys, ", ")))
		}
	}
	for _, k := range []string{"PPLX_BASE_URL", "PPLX_API_VERSION", "PPLX_SESSION_COOKIE_NAME", "PPLX_ENDPOINT_ASK"} {
		if os.Getenv(k) != "" {
			out = append(out, "env "+k)
		}
	}
	if out == nil {
		out = append(out, "none (embedded spec)")
	}
	return out
}

// --- upstream parsing -------------------------------------------------------

var (
	reFinalStr   = regexp.MustCompile(`^([A-Z_]+):\s*Final\[str\]\s*=\s*"([^"]*)"`)
	reEndpointID = regexp.MustCompile(`^ENDPOINT_([A-Z_]+):\s*Final\[str\]\s*=\s*"([^"]*)"`)
	reModel      = regexp.MustCompile(`^\s*(\w+)\s*=\s*Model\(identifier="([^"]+)"(?:,\s*mode="([^"]+)")?\)`)
)

// endpointKeyMap translates upstream ENDPOINT_* names to spec keys.
var endpointKeyMap = map[string]string{
	"ASK":           "ask",
	"SEARCH_INIT":   "search_init",
	"UPLOAD":        "upload",
	"RATE_LIMITS":   "rate_limits",
	"USER_SETTINGS": "user_settings",
	"LIST_THREADS":  "list_threads",
	"THREAD_DETAIL": "thread_detail",
	"CREDITS":       "credits",
}

// upstreamPatch is the candidate change set extracted from upstream sources.
type upstreamPatch struct {
	APIVersion        string
	BaseURL           string
	SessionCookieName string
	Endpoints         map[string]string // spec key -> upstream path
	ModelIdentifiers  []string
}

func parseUpstream(constantsPy, modelsPy string) *upstreamPatch {
	p := &upstreamPatch{Endpoints: map[string]string{}}
	for _, line := range strings.Split(constantsPy, "\n") {
		line = strings.TrimSpace(line)
		if m := reEndpointID.FindStringSubmatch(line); m != nil {
			if key, ok := endpointKeyMap[m[1]]; ok {
				p.Endpoints[key] = m[2]
			}
			continue
		}
		if m := reFinalStr.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "API_VERSION":
				p.APIVersion = m[2]
			case "API_BASE_URL":
				p.BaseURL = m[2]
			case "SESSION_COOKIE_NAME":
				p.SessionCookieName = m[2]
			}
		}
	}
	for _, line := range strings.Split(modelsPy, "\n") {
		if m := reModel.FindStringSubmatch(line); m != nil {
			p.ModelIdentifiers = append(p.ModelIdentifiers, m[2])
		}
	}
	return p
}

// --- sync -------------------------------------------------------------------

func cmdSpecSync(args []string) error {
	fs := flag.NewFlagSet("spec sync", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "write overrides without prompting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	urls := syncSourceURLs()
	constantsPy, err := fetchUpstream(ctx, urls["constants"])
	if err != nil {
		return err
	}
	modelsPy, err := fetchUpstream(ctx, urls["models"])
	if err != nil {
		return err
	}

	sp, err := spec.Load()
	if err != nil {
		return err
	}
	var base spec.Spec
	if err := json.Unmarshal(sp.Raw, &base); err != nil {
		return fmt.Errorf("parse embedded spec: %w", err)
	}

	patch := parseUpstream(constantsPy, modelsPy)
	changes := diffPatch(&base, patch)
	if len(changes) == 0 {
		fmt.Fprintln(stdout, "Spec is in sync with upstream.")
		return nil
	}
	fmt.Fprintf(stdout, "Upstream drift detected (%d change(s)):\n", len(changes))
	for _, c := range changes {
		fmt.Fprintf(stdout, "  %s: %q -> %q\n", c.key, c.from, c.to)
	}
	for _, m := range addedModels(&base, patch) {
		fmt.Fprintf(stdout, "  + model identifier %q (informational; models are not persisted by sync)\n", m)
	}

	if !*yes {
		ans, err := prompt(fmt.Sprintf("Write these overrides to %s? [y/N] ", spec.OverridesFile))
		if err != nil {
			return err
		}
		if !strings.EqualFold(ans, "y") && !strings.EqualFold(ans, "yes") {
			fmt.Fprintln(stdout, "Declined; nothing written.")
			return nil
		}
	}
	if err := writeOverrides(patch, changes); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Wrote overrides to %s\n", spec.OverridesFile)
	return nil
}

type specChange struct{ key, from, to string }

func diffPatch(base *spec.Spec, p *upstreamPatch) []specChange {
	var out []specChange
	add := func(key, from, to string) {
		if to != "" && to != from {
			out = append(out, specChange{key, from, to})
		}
	}
	add("api_version", base.APIVersion, p.APIVersion)
	add("base_url", base.BaseURL, p.BaseURL)
	add("session_cookie_name", base.SessionCookieName, p.SessionCookieName)
	keys := make([]string, 0, len(p.Endpoints))
	for k := range p.Endpoints {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add("endpoints."+k, base.Endpoints.Get(k), p.Endpoints[k])
	}
	return out
}

func addedModels(base *spec.Spec, p *upstreamPatch) []string {
	have := map[string]bool{}
	for _, m := range base.Models {
		have[m.Identifier] = true
	}
	var out []string
	for _, id := range p.ModelIdentifiers {
		if !have[id] {
			out = append(out, id)
		}
	}
	return out
}

// writeOverrides merges the patch into the existing overrides file (if any)
// so unrelated manual overrides survive a sync.
func writeOverrides(p *upstreamPatch, changes []specChange) error {
	changed := map[string]bool{}
	for _, c := range changes {
		changed[c.key] = true
	}
	ov := map[string]any{}
	if data, err := os.ReadFile(spec.OverridesFile); err == nil {
		_ = json.Unmarshal(data, &ov) // best effort; invalid file is replaced
	}
	if changed["api_version"] {
		ov["api_version"] = p.APIVersion
	}
	if changed["base_url"] {
		ov["base_url"] = p.BaseURL
	}
	if changed["session_cookie_name"] {
		ov["session_cookie_name"] = p.SessionCookieName
	}
	var endpoints map[string]any
	if raw, ok := ov["endpoints"].(map[string]any); ok {
		endpoints = raw
	} else {
		endpoints = map[string]any{}
	}
	for key, path := range p.Endpoints {
		if changed["endpoints."+key] {
			endpoints[key] = path
		}
	}
	if len(endpoints) > 0 {
		ov["endpoints"] = endpoints
	}
	data, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepathDir(spec.OverridesFile), 0o700); err != nil {
		return err
	}
	return os.WriteFile(spec.OverridesFile, append(data, '\n'), 0o600)
}

func filepathDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "."
	}
	return p[:i]
}

// --- check ------------------------------------------------------------------

func cmdSpecCheck(args []string) error {
	fs := flag.NewFlagSet("spec check", flag.ContinueOnError)
	live := fs.Bool("live", false, "run the real smoke test (hits Perplexity)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sp, err := loadSpec()
	if err != nil {
		return err
	}
	if !*live {
		fmt.Fprintln(stdout, "pplx spec check --live would test, in order:")
		fmt.Fprintf(stdout, "  1. GET  %s   (rate limits)\n", sp.Endpoints.RateLimits)
		fmt.Fprintf(stdout, "  2. POST %s   (ask, query \"ping\")\n", sp.Endpoints.Ask)
		fmt.Fprintf(stdout, "  3. POST %s  (thread list)\n", sp.Endpoints.ListThreads)
		fmt.Fprintln(stdout, "No network calls made. Re-run with --live to smoke test.")
		return nil
	}

	token, err := loadToken()
	if err != nil {
		return err
	}
	t, err := newTransport(sp)
	if err != nil {
		return err
	}
	t.SetCookie(sp.SessionCookieName, token)
	ctx := context.Background()

	type result struct {
		name   string
		status string
		err    error
	}
	var results []result

	if _, err := pplx.Usage(ctx, t, sp); err != nil {
		results = append(results, result{"rate-limits", statusOf(err), err})
	} else {
		results = append(results, result{"rate-limits", "200", nil})
	}

	conv := pplx.NewConversation(t, sp)
	if _, err := conv.Ask(ctx, "ping", pplx.AskOptions{Model: "auto"}); err != nil {
		results = append(results, result{"ask", statusOf(err), err})
	} else {
		results = append(results, result{"ask", "200", nil})
	}

	var threadList any
	if err := t.PostJSON(ctx, sp.Endpoints.ListThreads, map[string]any{"limit": 1, "offset": 0, "search_term": ""}, &threadList); err != nil {
		results = append(results, result{"thread-list", statusOf(err), err})
	} else {
		results = append(results, result{"thread-list", "200", nil})
	}

	failed := 0
	for _, r := range results {
		if r.err == nil {
			fmt.Fprintf(stdout, "PASS %-14s %s\n", r.name, r.status)
		} else {
			failed++
			fmt.Fprintf(stdout, "FAIL %-14s %s: %v\n", r.name, r.status, r.err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d/%d endpoints failed", failed, len(results))
	}
	fmt.Fprintln(stdout, "All endpoints OK.")
	return nil
}

func statusOf(err error) string {
	var se *transport.StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("HTTP %d", se.StatusCode)
	}
	return "ERR"
}
