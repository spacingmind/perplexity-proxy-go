package pplx

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

var (
	ErrTOTPRequired = errors.New("TOTP verification required: provide a 6-digit code")
	ErrNoCSRF       = errors.New("failed to obtain CSRF token")
)

// Auth implements the email OTP + optional TOTP sign-in flow against the
// Perplexity web app, ported from the reference auth.py. Two phases so the
// CLI can prompt for the emailed code in between:
//
//	a.RequestCode(ctx, email)        // sends the numeric code by email
//	a.CompleteLogin(ctx, email, otp) // exchanges the code for a session token
type Auth struct {
	t    transport.Client
	sp   *spec.Spec
	csrf string
}

func NewAuth(t transport.Client, sp *spec.Spec) *Auth {
	return &Auth{t: t, sp: sp}
}

// RequestCode warms the session, fetches a CSRF token, and asks Perplexity
// to email a numeric verification code.
func (a *Auth) RequestCode(ctx context.Context, email string) error {
	// Visit the app first so Cloudflare/session cookies exist (auth.py does
	// the same via session.get(API_BASE_URL)).
	if err := a.t.Get(ctx, "/"); err != nil {
		return fmt.Errorf("warm up %s: %w", a.sp.BaseURL, err)
	}
	csrf, err := a.fetchCSRF(ctx)
	if err != nil {
		return err
	}
	a.csrf = csrf

	var out struct {
		Error string `json:"error"`
	}
	path := fmt.Sprintf("%s?version=%s&source=default", a.sp.Endpoints.AuthSigninEmail, url.QueryEscape(a.sp.APIVersion))
	if err := a.t.PostJSON(ctx, path, map[string]any{
		"email":         email,
		"csrfToken":     csrf,
		"useNumericOtp": "true",
		"json":          "true",
		"callbackUrl":   a.sp.BaseURL + "/?login-source=floatingSignup",
	}, &out); err != nil {
		return fmt.Errorf("request verification code: %w", err)
	}
	if out.Error != "" {
		return fmt.Errorf("request verification code: %s", out.Error)
	}
	return nil
}

func (a *Auth) fetchCSRF(ctx context.Context) (string, error) {
	var out struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := a.t.GetJSON(ctx, a.sp.Endpoints.AuthCSRF, &out); err != nil {
		return "", fmt.Errorf("fetch CSRF: %w", err)
	}
	if out.CSRFToken == "" {
		return "", ErrNoCSRF
	}
	return out.CSRFToken, nil
}

// CompleteLogin resolves the emailed OTP (or magic link) to a session token.
// totpCode completes a TOTP challenge when Perplexity requires one; pass ""
// when not prompted. Returns the session token value.
func (a *Auth) CompleteLogin(ctx context.Context, email, codeOrLink, totpCode string) (string, error) {
	if a.csrf == "" {
		csrf, err := a.fetchCSRF(ctx)
		if err != nil {
			return "", err
		}
		a.csrf = csrf
	}

	redirectURL, err := a.resolveCode(ctx, email, codeOrLink)
	if err != nil {
		return "", err
	}
	if err := a.followCallback(ctx, redirectURL, totpCode); err != nil {
		return "", err
	}

	token := a.t.Cookie(a.sp.SessionCookieName)
	if token == "" {
		return "", errors.New("authentication completed but no session token cookie was returned")
	}
	return token, nil
}

// resolveCode exchanges the numeric OTP for the auth callback URL. A full
// http(s) link is passed through unchanged (magic-link login).
func (a *Auth) resolveCode(ctx context.Context, email, codeOrLink string) (string, error) {
	if strings.HasPrefix(codeOrLink, "http://") || strings.HasPrefix(codeOrLink, "https://") {
		return codeOrLink, nil
	}
	var out struct {
		Redirect string `json:"redirect"`
	}
	if err := a.t.PostJSON(ctx, a.sp.Endpoints.AuthOTPRedirect, map[string]any{
		"email":            email,
		"otp":              codeOrLink,
		"redirectUrl":      a.sp.BaseURL + "/?login-source=floatingSignup",
		"emailLoginMethod": "web-otp",
	}, &out); err != nil {
		return "", fmt.Errorf("verify code: %w", err)
	}
	if out.Redirect == "" {
		return "", errors.New("no redirect URL received for code")
	}
	if strings.HasPrefix(out.Redirect, "/") {
		return out.Redirect, nil
	}
	return out.Redirect, nil
}

// followCallback walks the auth callback redirect chain, handling the TOTP
// challenge branch, and leaves the session cookie in the transport jar.
func (a *Auth) followCallback(ctx context.Context, redirectURL string, totpCode string) error {
	status, location, err := a.t.GetNoRedirect(ctx, redirectURL)
	if err != nil {
		return fmt.Errorf("auth callback: %w", err)
	}
	if !isRedirect(status) {
		// 200: session cookie may already be set; nothing more to follow.
		return nil
	}
	if location == "" {
		return errors.New("auth callback did not provide a redirect location")
	}
	if strings.Contains(location, "error=") {
		return errors.New("verification failed: the code may be invalid or expired")
	}

	if strings.Contains(location, "/auth/totp-challenge") {
		challengeToken, err := totpTokenFromLocation(location)
		if err != nil {
			return err
		}
		return a.verifyTOTP(ctx, challengeToken, totpCode)
	}

	return a.t.Get(ctx, relativeOrAbsolute(location))
}

// verifyTOTP completes a TOTP challenge and follows its post-verify redirect.
func (a *Auth) verifyTOTP(ctx context.Context, challengeToken, totpCode string) error {
	if totpCode == "" {
		return ErrTOTPRequired
	}
	if len(totpCode) != 6 {
		return errors.New("TOTP code must be a 6-digit number")
	}
	if _, err := strconv.Atoi(totpCode); err != nil {
		return errors.New("TOTP code must be a 6-digit number")
	}

	var out struct {
		Error    string `json:"error"`
		Redirect string `json:"redirect"`
	}
	path := fmt.Sprintf("%s?version=%s&source=default", a.sp.Endpoints.AuthTOTPVerify, url.QueryEscape(a.sp.APIVersion))
	if err := a.t.PostJSON(ctx, path, map[string]any{
		"token": challengeToken,
		"code":  totpCode,
	}, &out); err != nil {
		return fmt.Errorf("TOTP verify: %w", err)
	}
	if out.Error != "" {
		return fmt.Errorf("TOTP verification failed: %s", out.Error)
	}
	if out.Redirect != "" {
		return a.t.Get(ctx, relativeOrAbsolute(out.Redirect))
	}
	return nil
}

func isRedirect(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

func totpTokenFromLocation(location string) (string, error) {
	u, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("parse TOTP challenge location: %w", err)
	}
	token := u.Query().Get("token")
	if token == "" {
		return "", errors.New("TOTP challenge token not found in redirect")
	}
	return token, nil
}

func relativeOrAbsolute(location string) string {
	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		return location
	}
	if !strings.HasPrefix(location, "/") {
		return "/" + location
	}
	return location
}
