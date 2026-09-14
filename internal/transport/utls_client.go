package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"

	utls "github.com/refraction-networking/utls"
)

// NewUTLS returns a Client whose TLS handshake presents a Chrome ClientHello
// via utls. Connections are pooled per host+protocol so a session (warm-up
// GET, then POST on the same tab-like connection) mirrors browser behavior —
// bot scoring flagged the previous fresh-connection-per-request pattern.
func NewUTLS(opt Options) (Client, error) {
	return newShared(&http.Client{
		Timeout:   opt.timeout(),
		Transport: &utlsRoundTripper{conns: map[string]pooledConn{}},
	}, opt, nil)
}

type pooledConn struct {
	t2 *http2TransportShim // non-nil if ALPN negotiated h2
	t1 *http.Transport     // non-nil for http/1.1
}

// http2TransportShim exists only to keep imports tidy; it wraps http2.Transport.
type http2TransportShim struct {
	inner interface {
		RoundTrip(*http.Request) (*http.Response, error)
	}
}

func (s *http2TransportShim) RoundTrip(req *http.Request) (*http.Response, error) {
	return s.inner.RoundTrip(req)
}

type utlsRoundTripper struct {
	mu    sync.Mutex
	conns map[string]pooledConn
}

var _ http.RoundTripper = (*utlsRoundTripper)(nil)

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if req.URL.Scheme != "https" {
		t1 := &http.Transport{}
		return t1.RoundTrip(req)
	}
	key := req.URL.Host
	t.mu.Lock()
	pc, ok := t.conns[key]
	t.mu.Unlock()

	if ok {
		if pc.t2 != nil {
			return pc.t2.RoundTrip(req)
		}
		if pc.t1 != nil {
			return pc.t1.RoundTrip(req)
		}
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
		pc = pooledConn{t2: &http2TransportShim{inner: t2}}
	} else {
		t1 := &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
			return conn, nil
		}}
		pc = pooledConn{t1: t1}
	}
	t.mu.Lock()
	t.conns[key] = pc
	t.mu.Unlock()
	return pc.RoundTrip(req)
}

func (pc pooledConn) RoundTrip(req *http.Request) (*http.Response, error) {
	if pc.t2 != nil {
		return pc.t2.RoundTrip(req)
	}
	return pc.t1.RoundTrip(req)
}

func (t *utlsRoundTripper) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, pc := range t.conns {
		if pc.t1 != nil {
			pc.t1.CloseIdleConnections()
		}
	}
	t.conns = map[string]pooledConn{}
}

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
	cfg := &utls.Config{ServerName: host}
	tlsConn := utls.UClient(conn, cfg, utls.HelloChrome_Auto)
	applyChromeHello(tlsConn)
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
