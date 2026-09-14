package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"

	utls "github.com/refraction-networking/utls"
)

// NewUTLS returns a Client whose TLS handshake presents a Chrome ClientHello
// via utls, hiding behind the Client interface so tests can swap in NewPlain.
//
// HTTP/2 must be layered on explicitly: the stdlib only auto-configures h2
// when it performs the TLS dial itself (ForceAttemptHTTP2 has no effect with
// a custom DialTLSContext). Speaking HTTP/1.x to Perplexity's h2 endpoints
// yields "malformed HTTP response" on the first binary SETTINGS frame —
// found via live smoke test, not caught by httptest (NewPlain).
func NewUTLS(opt Options) (Client, error) {
	return newShared(&http.Client{
		Timeout:   opt.timeout(),
		Transport: &utlsRoundTripper{},
	}, opt, nil)
}

// utlsRoundTripper dials each request with a Chrome-fingerprinted utls
// handshake, then serves the request over whichever protocol ALPN selected
// (h2 via http2.Transport, anything else via HTTP/1.1). Each RoundTrip gets
// a fresh connection: acceptable for a CLI's handful of requests per run,
// and it keeps h1/h2 selection trivially correct.
type utlsRoundTripper struct{}

var _ http.RoundTripper = (*utlsRoundTripper)(nil)

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if req.URL.Scheme != "https" {
		t1 := &http.Transport{}
		return t1.RoundTrip(req)
	}
	conn, err := dialUTLS(ctx, "tcp", canonicalAddr(req.URL))
	if err != nil {
		return nil, err
	}
	uconn, ok := conn.(*utls.UConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("unexpected connection type %T", conn)
	}
	if uconn.ConnectionState().NegotiatedProtocol == "h2" {
		t2 := newChromeH2Transport(func(context.Context, string, string, *tls.Config) (net.Conn, error) {
			return conn, nil
		})
		return t2.RoundTrip(req)
	}
	t1 := &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
		return conn, nil
	}}
	return t1.RoundTrip(req)
}

func (t *utlsRoundTripper) CloseIdleConnections() {}

func dialUTLS(ctx context.Context, network, addr string) (net.Conn, error) {
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
	tlsConn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloChrome_Auto)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
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
