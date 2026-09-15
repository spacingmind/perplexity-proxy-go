package transport

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"os"
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
	return dialUTLSOpt(ctx, network, addr, false)
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
	if insecure {
		cfg.InsecureSkipVerify = true
		cfg.NextProtos = []string{"h2"}
	}
	tlsConn := utls.UClient(conn, cfg, utls.HelloCustom)
	applyChromeHello(tlsConn)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
}
