// Package pplx implements the Perplexity web client: authentication,
// asking queries over SSE, and usage/rate-limit queries. All protocol
// values come from internal/spec; all HTTP goes through internal/transport.
package pplx

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultTokenTTL is how long a session token is considered fresh.
const DefaultTokenTTL = 30 * 24 * time.Hour

var (
	ErrNoToken      = errors.New("no Perplexity token found (run 'pplx login')")
	ErrTokenExpired = errors.New("Perplexity token expired (run 'pplx login')")
)

// Token is a persisted session token with freshness metadata.
type Token struct {
	Value      string    `json:"token"`
	ObtainedAt time.Time `json:"obtained_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// Fresh reports whether the token is present and unexpired at time now.
func (t *Token) Fresh(now time.Time) bool {
	return t != nil && t.Value != "" && now.Before(t.ExpiresAt)
}

// TokenStore persists the session token as JSON at a fixed path.
type TokenStore struct {
	path string
}

// NewTokenStore returns a store writing to path.
func NewTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

// DefaultTokenPath is ~/.pplx/token.json (or $PPLX_TOKEN_PATH when set).
func DefaultTokenPath() (string, error) {
	if p := os.Getenv("PPLX_TOKEN_PATH"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pplx", "token.json"), nil
}

// DefaultTokenStore is the production store at ~/.pplx/token.json.
func DefaultTokenStore() (*TokenStore, error) {
	path, err := DefaultTokenPath()
	if err != nil {
		return nil, err
	}
	return NewTokenStore(path), nil
}

// Save writes the token with obtained-at=now and expiry=ttl, creating the
// parent directory with 0700 and the file with 0600.
func (s *TokenStore) Save(value string, ttl time.Duration) (*Token, error) {
	if value == "" {
		return nil, errors.New("refusing to save empty token")
	}
	now := time.Now()
	tok := &Token{Value: value, ObtainedAt: now, ExpiresAt: now.Add(ttl)}
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("create token dir: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return nil, fmt.Errorf("write token: %w", err)
	}
	return tok, nil
}

// Load reads the token and checks its expiry.
func (s *TokenStore) Load() (*Token, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, ErrNoToken
	}
	if err != nil {
		return nil, fmt.Errorf("read token: %w", err)
	}
	var tok Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("parse token %s: %w", s.path, err)
	}
	if tok.Value == "" {
		return nil, ErrNoToken
	}
	if !tok.Fresh(time.Now()) {
		return nil, ErrTokenExpired
	}
	return &tok, nil
}
