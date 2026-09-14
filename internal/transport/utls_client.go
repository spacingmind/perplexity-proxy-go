package transport

import (
	"context"
	"net"
	"net/http"

	utls "github.com/refraction-networking/utls"
)

// NewUTLS returns a Client whose TLS handshake presents a Chrome ClientHello
// via utls, hiding behind the Client interface so tests can swap in NewPlain.
func NewUTLS(opt Options) (Client, error) {
	tr := &http.Transport{
		// ForceAttemptHTTP2 lets the stdlib negotiate h2 over our custom
		// dialer (utls advertises h2 in its ALPN extension).
		ForceAttemptHTTP2: true,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := net.Dial(network, addr)
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
		},
	}
	return newShared(&http.Client{
		Timeout:   opt.timeout(),
		Transport: tr,
	}, opt, nil)
}
