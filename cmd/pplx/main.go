// Command pplx is a CLI for the unofficial Perplexity web API.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
)

const usageText = `pplx — Perplexity web API client

Usage:
  pplx login                              sign in with email + OTP (TOTP supported)
  pplx ask "query" [-m model] [-s source] ask one question, print answer + citations
      -m model    model name or identifier (see 'pplx spec show')
      -s source   source focus: web, scholar, social, edgar
      --no-citations  print answer only
  pplx usage                              print remaining rate limits
  pplx spec show|sync|check               inspect or update the protocol spec
  pplx mcp                                MCP server over stdio (tools: ask, research, usage, check)
  pplx serve [--addr] [--api-key]         Anthropic-compatible /v1/messages server
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "pplx: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "login":
		return cmdLogin(rest)
	case "ask":
		return cmdAsk(rest)
	case "bridge":
		if len(rest) > 0 && rest[0] == "setup" {
			return cmdBridgeSetup(rest[1:])
		}
		return errors.New("usage: pplx bridge setup")
	case "dump":
		return cmdDump(args[1:])
	case "usage":
		return cmdUsage(rest)
	case "spec":
		return cmdSpec(rest)
	case "mcp":
		return cmdMCP(rest)
	case "serve":
		return cmdServe(rest)
	case "help", "-h", "--help":
		fmt.Print(usageText)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usageText)
	}
}

// loadSpec loads the effective spec with a friendly error on misconfiguration.
func loadSpec() (*spec.Spec, error) {
	sp, err := spec.Load()
	if err != nil {
		return nil, fmt.Errorf("load spec: %w", err)
	}
	return sp, nil
}
