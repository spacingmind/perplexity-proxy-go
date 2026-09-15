package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"github.com/spacingmind/perplexity-proxy-go/internal/api"
	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// cmdServe runs the Anthropic-compatible /v1/messages server. Production
// wiring mirrors the other commands: spec + token store + transport seam.
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	apiKey := fs.String("api-key", "", "require this bearer/x-api-key (empty = no auth, loopback)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	srv := api.New(func() (transport.Client, *spec.Spec, error) {
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
	}, *apiKey)

	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler()}
	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "pplx serve listening on http://%s/v1/messages\n", *addr)
		errCh <- httpSrv.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stop:
		fmt.Fprintln(os.Stderr, "shutting down")
		return httpSrv.Shutdown(context.Background())
	}
	return nil
}
