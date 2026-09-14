// Package transport provides the HTTP layer for talking to Perplexity.
// The Client interface keeps the transport swappable: internal/pplx depends
// only on the interface, tests use the plain net/http implementation against
// httptest servers, and production uses the utls-fingerprinted implementation.
package transport

import (
	"context"
)

// Client is the minimal HTTP surface internal/pplx needs. Paths are endpoint
// paths from the spec (e.g. "/rest/rate-limit/all"), resolved against the
// spec base URL; absolute URLs are also accepted.
type Client interface {
	// Get performs a status-check GET with minimal browser headers
	// (used for search session init).
	Get(ctx context.Context, path string) error
	// GetJSON performs a GET with full app headers and decodes a 200
	// response body as JSON into out. out may be nil.
	GetJSON(ctx context.Context, path string, out any) error
	// GetNoRedirect performs a GET without following redirects and returns
	// the response status and Location header ("" when absent).
	// Used by the auth callback/TOTP flow.
	GetNoRedirect(ctx context.Context, path string) (status int, location string, err error)
	// PostJSON performs a POST with full app headers and decodes a 200
	// response body as JSON into out. out may be nil.
	PostJSON(ctx context.Context, path string, body, out any) error
	// PostSSE performs a POST with full app headers and invokes onLine for
	// each line of the streamed response body. Returning an error from
	// onLine aborts the stream and is returned to the caller.
	PostSSE(ctx context.Context, path string, body any, onLine func(line []byte) error) error

	// SetCookie installs a session-style cookie on the underlying jar.
	SetCookie(name, value string)
	// Cookie returns the cookie value, reassembling chunked cookies
	// (name.0, name.1, ...) when the whole cookie is absent.
	Cookie(name string) string
}
