package main

import (
	"context"
	"errors"
	"os"

	"github.com/spacingmind/perplexity-proxy-go/internal/mcp"
	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// cmdMCP runs the stdio MCP server. Production wiring mirrors the ask/usage
// commands: effective spec, session token from the store, utls transport.
// Logs must go to stderr only — stdout is the protocol channel.
func cmdMCP(args []string) error {
	if len(args) > 0 {
		return errors.New("usage: pplx mcp")
	}
	srv := mcp.New(func() (transport.Client, *spec.Spec, error) {
		sp, err := loadSpec()
		if err != nil {
			return nil, nil, err
		}
		token, err := loadToken()
		if err != nil {
			return nil, nil, err
		}
		t, err := newTransport(sp)
		if err != nil {
			return nil, nil, err
		}
		t.SetCookie(sp.SessionCookieName, token)
		return t, sp, nil
	})
	return srv.Run(context.Background(), os.Stdin, os.Stdout)
}
