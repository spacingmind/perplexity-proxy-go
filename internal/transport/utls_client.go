package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	http2 "github.com/spacingmind/perplexity-proxy-go/internal/http2x"
)

// NewUTLS returns a Client whose TLS handshake presents the captured Chrome
// 150 ClientHello via utls and speaks HTTP/2 through the patched http2x
// fork (Chrome byte-exact frames) or HTTP/1.1 when ALPN selects it.
//
// A single http2.Transport owns connection pooling: it re-dials through
// dialUTLS per connection but keeps connections alive across requests,
// which is both correct (multiplexed streams share one conn) and
// browser-like (warm connection reuse).
func NewUTLS(opt Options) (Client, error) {
	t2 := newChromeH2Transport(func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		return dialUTLS(ctx, network, addr)
	})
	t1 := &http.Transport{DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialUTLS(ctx, network, addr)
	}}
	return newShared(&http.Client{
		Timeout:   opt.timeout(),
		Transport: &alpnRoundTripper{t2: t2, t1: t1},
	}, opt, nil)
}

// alpnRoundTripper dials once with utls, then serves the request over the
// protocol ALPN selected. h2 connections are owned by the shared http2
// Transport (pooled); h1 falls back to a plain Transport.
type alpnRoundTripper struct {
	t2 *http2.Transport
	t1 *http.Transport
}

var _ http.RoundTripper = (*alpnRoundTripper)(nil)

func (a *alpnRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return a.t1.RoundTrip(req)
	}
	if os.Getenv("PPLX_FRESH_CONN") == "1" {
		// A/B experiment: fresh connection per request (the shape that
		// measured 8/8 live passes).
		t2 := newChromeH2Transport(func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return dialUTLS(ctx, network, addr)
		})
		return t2.RoundTrip(req)
	}
	return a.t2.RoundTrip(req)
}

func (a *alpnRoundTripper) CloseIdleConnections() {
	a.t2.CloseIdleConnections()
	a.t1.CloseIdleConnections()
}

func dialUTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := dialUTLSOpt(ctx, network, addr, false)
	if err == nil {
		return conn, nil
	}
	if !isHandshakeFailure(err) {
		return nil, err
	}
	// Self-heal: a failing static ClientHello is likely fingerprint-burned.
	// Try to capture a fresh one (curl_cffi shuffles extensions per session)
	// and retry the handshake once.
	if healErr := refreshCapturedHello(); healErr == nil {
		RefreshHello()
		if conn2, err2 := dialUTLSOpt(ctx, network, addr, false); err2 == nil {
			return conn2, nil
		}
	}
	return nil, err
}

func isHandshakeFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "handshake failure") || strings.Contains(msg, "HandshakeFailure")
}

// refreshCapturedHello runs the capture helper (curl_cffi) to produce a
// fresh ClientHello at the runtime hello path.
func refreshCapturedHello() error {
	script := os.Getenv("PPLX_CAPTURE_SCRIPT")
	if script == "" {
		script = filepath.Join(sourceDir(), "scripts", "capture_hello.py")
	}
	py := os.Getenv("PPLX_PYTHON")
	if py == "" {
		py = "python3"
	}
	out := helloFile()
	if out == "" {
		return errors.New("no hello file path")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, script, out)
	return cmd.Run()
}

// sourceDir best-effort locates the repo scripts dir for the capture helper
// (executable-relative lookup is unreliable; env override exists).
func sourceDir() string {
	if v := os.Getenv("PPLX_HOME_DIR"); v != "" {
		return v
	}
	return "."
}

func canonicalAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// NewDebugTransport exposes the production roundtripper (utls + http2x fork)
// against an arbitrary base URL with certificate verification disabled, for
// wire-frame probing against a local sink.
func NewDebugTransport(baseURL string) *http.Client {
	t2 := newChromeH2Transport(func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		return dialUTLSOpt(ctx, network, addr, true)
	})
	t1 := &http.Transport{DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialUTLSOpt(ctx, network, addr, true)
	}}
	_ = t1
	return &http.Client{
		Transport: &debugRT{t2: t2},
		Timeout:   10 * time.Second,
	}
}

type debugRT struct{ t2 *http2.Transport }

func (d *debugRT) RoundTrip(req *http.Request) (*http.Response, error) { return d.t2.RoundTrip(req) }

func dialUTLSOpt(ctx context.Context, network, addr string, insecure bool) (net.Conn, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	cfg := &utls.Config{ServerName: host}
	if os.Getenv("PPLX_DEBUG_DIAL") == "1" {
		fmt.Fprintf(os.Stderr, "dialUTLSOpt addr=%q host=%q insecure=%v\n", addr, host, insecure)
	}
	if insecure {
		cfg.InsecureSkipVerify = true
		cfg.NextProtos = []string{"h2"}
	}
	// Captured Chrome 150 ClientHello (data/chrome150_clienthello.bin) via
	// HelloCustom + per-call FromRaw. Verified live: passes fraud scoring
	// (4/4) where HelloChrome_Auto (133) fails 0/4, and the ML-KEM
	// key_share must stay intact (dropping it = handshake failure).
	tlsConn := utls.UClient(conn, cfg, utls.HelloCustom)
	applyChromeHello(tlsConn)
	err = tlsConn.HandshakeContext(ctx)
	if os.Getenv("PPLX_DEBUG_DIAL") == "1" {
		fmt.Fprintf(os.Stderr, "handshake addr=%q err=%v alpn=%q\n", addr, err, tlsConn.ConnectionState().NegotiatedProtocol)
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
}
