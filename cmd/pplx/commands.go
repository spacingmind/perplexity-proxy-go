package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spacingmind/perplexity-proxy-go/internal/pplx"
	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// newTransport is the seam between production (utls fingerprint) and tests
// (plain net/http against httptest). Tests swap it for a factory pointing
// at the test server.
var newTransport = func(sp *spec.Spec) (transport.Client, error) {
	return transport.NewUTLS(transport.Options{
		BaseURL:    sp.BaseURL,
		APIVersion: sp.APIVersion,
		Timeout:    5 * time.Minute,
	})
}

// stdin is the seam for prompt input; tests inject a strings.Reader.
var stdin io.Reader = os.Stdin

// stdout is the seam for command output; tests capture it.
var stdout io.Writer = os.Stdout

func prompt(label string) (string, error) {
	fmt.Print(label)
	r := bufio.NewReader(stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	var email string
	fs.StringVar(&email, "email", "", "account email (prompted when omitted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	sp, err := loadSpec()
	if err != nil {
		return err
	}
	t, err := newTransport(sp)
	if err != nil {
		return err
	}

	if email == "" {
		email, err = prompt("Email: ")
		if err != nil || email == "" {
			return errors.New("email is required")
		}
	}

	auth := pplx.NewAuth(t, sp)
	fmt.Fprintf(os.Stderr, "Sending verification code to %s...\n", email)
	if err := auth.RequestCode(ctx, email); err != nil {
		return friendlyErr(err)
	}
	code, err := prompt("Verification code (or magic link): ")
	if err != nil || code == "" {
		return errors.New("verification code is required")
	}

	token, err := auth.CompleteLogin(ctx, email, code, "")
	if errors.Is(err, pplx.ErrTOTPRequired) {
		var totp string
		totp, err = prompt("Authenticator code (6 digits): ")
		if err != nil || totp == "" {
			return errors.New("TOTP code is required")
		}
		// Fresh callback: CompleteLogin re-walks the otp -> callback chain.
		token, err = auth.CompleteLogin(ctx, email, code, totp)
	}
	if err != nil {
		return friendlyErr(err)
	}

	store, err := pplx.DefaultTokenStore()
	if err != nil {
		return err
	}
	tok, err := store.Save(token, pplx.DefaultTokenTTL)
	if err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	fmt.Printf("Logged in. Session valid until %s.\n", tok.ExpiresAt.Format("Jan 2, 2006"))
	return nil
}

func loadToken() (string, error) {
	store, err := pplx.DefaultTokenStore()
	if err != nil {
		return "", err
	}
	tok, err := store.Load()
	if err != nil {
		if errors.Is(err, pplx.ErrNoToken) || errors.Is(err, pplx.ErrTokenExpired) {
			return "", fmt.Errorf("%w", err)
		}
		return "", err
	}
	return tok.Value, nil
}

func cmdAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	model := fs.String("m", "", "model name or identifier")
	source := fs.String("s", "", "source focus (web, scholar, social, edgar)")
	noCitations := fs.Bool("no-citations", false, "print answer without citations")
	if err := fs.Parse(args); err != nil {
		return err
	}
	query := strings.Join(fs.Args(), " ")
	// flag stops at the first positional; pick up trailing flags
	// (pplx ask "q" -m best) with a second pass over the leftovers.
	if trailing := fs.Args(); len(trailing) > 1 {
		fs.Parse(trailing[1:]) // flags after the query; errors already surfaced
		query = trailing[0]
	}
	if query == "" {
		return errors.New("usage: pplx ask \"query\" [-m model] [-s source]")
	}

	sp, err := loadSpec()
	if err != nil {
		return err
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
	conv := pplx.NewConversation(t, sp)
	ans, err := conv.Ask(ctx, query, pplx.AskOptions{Model: *model, SourceFocus: *source})
	if err != nil {
		return friendlyErr(err)
	}
	printAnswer(stdout, ans, !*noCitations)
	return nil
}

func printAnswer(w io.Writer, ans *pplx.Answer, withCitations bool) {
	if ans == nil {
		return
	}
	if ans.Text == "" {
		fmt.Fprintln(w, "(no answer returned)")
	} else {
		fmt.Fprintln(w, ans.Text)
	}
	if withCitations && len(ans.Citations) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Sources:")
		for _, c := range ans.Citations {
			if c.Title != "" && c.URL != "" {
				fmt.Fprintf(w, "[%d] %s — %s\n", c.Index, c.Title, c.URL)
			} else if c.URL != "" {
				fmt.Fprintf(w, "[%d] %s\n", c.Index, c.URL)
			} else {
				fmt.Fprintf(w, "[%d] %s\n", c.Index, c.Title)
			}
		}
	}
}

func cmdUsage(args []string) error {
	if len(args) > 0 {
		return errors.New("usage: pplx usage")
	}
	sp, err := loadSpec()
	if err != nil {
		return err
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

	rl, err := pplx.Usage(context.Background(), t, sp)
	if err != nil {
		return friendlyErr(err)
	}
	printUsage(stdout, rl)
	return nil
}

func printUsage(w io.Writer, rl *pplx.RateLimits) {
	fmt.Fprintf(w, "Pro Search:          %d remaining\n", rl.RemainingPro)
	fmt.Fprintf(w, "Deep Research:       %d remaining\n", rl.RemainingResearch)
	fmt.Fprintf(w, "Create Files & Apps: %d remaining\n", rl.RemainingLabs)
	fmt.Fprintf(w, "Browser Agent:       %d remaining\n", rl.RemainingAgenticResearch)
	for _, s := range rl.SourceLimits {
		if s.Unlimited() {
			continue
		}
		rem := "—"
		if s.Remaining != nil {
			rem = fmt.Sprintf("%d/%d", *s.Remaining, *s.MonthlyLimit)
		}
		fmt.Fprintf(w, "  source %s: %s\n", s.SourceID, rem)
	}
}

// friendlyErr turns client failures into actionable messages.
func friendlyErr(err error) error {
	var se *transport.StatusError
	switch {
	case errors.Is(err, pplx.ErrRateLimited):
		return fmt.Errorf("rate limited by Perplexity — wait for the quota to reset or upgrade your plan")
	case errors.Is(err, pplx.ErrTOTPRequired):
		return err
	case errors.As(err, &se):
		switch se.StatusCode {
		case 401, 403:
			return fmt.Errorf("not authenticated (HTTP %d) — run 'pplx login'", se.StatusCode)
		case 429:
			return fmt.Errorf("rate limited (HTTP 429) — try again later")
		}
		return fmt.Errorf("HTTP %d: %s", se.StatusCode, truncate(se.Body, 200))
	default:
		return err
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
