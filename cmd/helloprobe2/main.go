package main

import (
	"context"
	"fmt"
	"net"

	utls "github.com/refraction-networking/utls"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

func main() {
	// A/B through the production dial with different hello sources
	fmt.Println("prod dial (runtime file if any):", transport.ProbeHandshakeReal("www.perplexity.ai:443", 0))
	// Direct: HelloChrome_Auto via HelloCustom+UTLSIdToSpec (same key path as our preset)
	d := probeAutoSpec("www.perplexity.ai:443")
	fmt.Println("autospec via custom:", d)
}

func probeAutoSpec(hostPort string) error {
	conn, err := netDial(hostPort)
	if err != nil {
		return err
	}
	spec, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err != nil {
		conn.Close()
		return err
	}
	host, _, _ := splitHP(hostPort)
	tlsConn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloCustom)
	if err := tlsConn.ApplyPreset(&spec); err != nil {
		conn.Close()
		return err
	}
	if err := tlsConn.HandshakeContext(bgCtx()); err != nil {
		conn.Close()
		return err
	}
	tlsConn.Close()
	return nil
}

func netDial(hostPort string) (net.Conn, error) {
	return net.Dial("tcp", hostPort)
}
func splitHP(hp string) (string, string, error) { return net.SplitHostPort(hp) }
func bgCtx() context.Context                    { return context.Background() }
