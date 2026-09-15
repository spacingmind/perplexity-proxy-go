package transport

import (
	"context"
	_ "embed"
	"net"
	"os"
	"path/filepath"
	"sync"

	utls "github.com/refraction-networking/utls"
)

//go:embed data/chrome150_clienthello.bin
var chrome150HelloRaw []byte

// applyChromeHello configures conn with the captured spec. The UConn must
// be constructed with utls.HelloCustom — utls regenerates the handshake for
// known IDs like HelloChrome_Auto during HandshakeContext, silently
// overwriting any preset applied earlier. Falls back to HelloChrome_Auto's
// spec when the capture cannot be applied.
// helloFile is the runtime source for a freshly captured ClientHello,
// taking precedence over the embedded (static, eventually burned) capture.
// curl_cffi shuffles extension order and randomizes GREASE per session, so
// a static replay gets fingerprint-blocked after a while.
func helloFile() string {
	if v := os.Getenv("PPLX_HELLO_FILE"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pplx", "clienthello.bin")
}

var (
	helloMu   sync.Mutex
	helloSpec *utls.ClientHelloSpec
	helloSrc  string // which source helloSpec came from
)

// loadHelloSpec returns a hybrid spec: the extension LAYOUT from the
// captured Chrome 150 hello (runtime file, else embedded) — extension
// order, ALPS values, cipher list — with the key-bearing extensions
// (key_share, supported_groups) swapped in from a code-generated spec whose
// keys utls generates fresh per handshake. Replaying a captured key_share
// cannot work: the process lacks the private keys, and utls never
// regenerates shares imported via FromRaw — the handshake fails after the
// peer accepts the hello.
func loadHelloSpec() *utls.ClientHelloSpec {
	helloMu.Lock()
	defer helloMu.Unlock()
	src := helloFile()
	if helloSpec != nil && helloSrc == src {
		return helloSpec
	}
	raw := chrome150HelloRaw
	if src != "" {
		if b, err := os.ReadFile(src); err == nil {
			raw = b
		}
	}
	if spec := buildHybridSpec(raw); spec != nil {
		helloSpec = spec
		helloSrc = src
		return spec
	}
	return nil
}

// buildHybridSpec: layout from raw, keys from UTLSIdToSpec(HelloChrome_Auto).
func buildHybridSpec(raw []byte) *utls.ClientHelloSpec {
	layout := &utls.ClientHelloSpec{}
	if err := layout.FromRaw(raw, true); err != nil {
		return nil
	}
	keyed, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err != nil {
		return nil
	}
	var liveKS, liveCurves any
	for _, e := range keyed.Extensions {
		if k, ok := e.(*utls.KeyShareExtension); ok && liveKS == nil {
			liveKS = k
		}
		if c, ok := e.(*utls.SupportedCurvesExtension); ok && liveCurves == nil {
			liveCurves = c
		}
	}
	if liveKS == nil {
		return nil
	}
	// Replace or append the key extensions in the layout.
	out := layout
	out.Extensions = out.Extensions[:0:0]
	replacedKS, replacedCurves := false, false
	for _, e := range layout.Extensions {
		switch e.(type) {
		case *utls.KeyShareExtension:
			if !replacedKS {
				out.Extensions = append(out.Extensions, liveKS.(*utls.KeyShareExtension))
				replacedKS = true
			}
		case *utls.SupportedCurvesExtension:
			if !replacedCurves && liveCurves != nil {
				out.Extensions = append(out.Extensions, liveCurves.(*utls.SupportedCurvesExtension))
				replacedCurves = true
			}
		default:
			out.Extensions = append(out.Extensions, e)
		}
	}
	if !replacedKS {
		out.Extensions = append(out.Extensions, liveKS.(*utls.KeyShareExtension))
	}
	return out
}

// RefreshHello re-reads the runtime hello file (or embedded) on next use.
// Call after regenerating the file.
func RefreshHello() {
	helloMu.Lock()
	defer helloMu.Unlock()
	helloSpec = nil
	helloSrc = ""
}

// applyChromeHello applies the fresh-or-embedded captured spec. UConn must
// be built with utls.HelloCustom (see dialUTLSOpt).
func applyChromeHello(conn *utls.UConn) {
	if spec := loadHelloSpec(); spec != nil {
		if err := conn.ApplyPreset(spec); err == nil {
			return
		}
	}
	fallback, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err == nil {
		_ = conn.ApplyPreset(&fallback)
	}
}

// ProbeHandshake dials addr with the production Chrome-150 dial, captures the
// ClientHello record via a memory pipe, and writes it to outFile ("" = skip).
// Debug helper for cmd/helloprobe.
func ProbeHandshake(addr, outFile string) error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer l.Close()
	type result struct {
		hello []byte
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			ch <- result{nil, err}
			return
		}
		buf := make([]byte, 4096)
		n, _ := c.Read(buf)
		c.Close()
		ch <- result{buf[:n], nil}
	}()
	// dial against our own listener: the TLS handshake writes the ClientHello
	// immediately, we capture it, then fail (no TLS server) — fine.
	_, derr := dialUTLS(context.Background(), "tcp", addrForProbe(l.Addr().String()))
	if outFile != "" {
		r := <-ch
		if r.err == nil {
			return os.WriteFile(outFile, r.hello, 0644)
		}
		return r.err
	}
	return derr
}

func addrForProbe(listenAddr string) string { return listenAddr }

// ProbeHandshakeReal performs the production TLS handshake against a real
// host and reports the error (nil = handshake completed). Debug helper.
func ProbeHandshakeReal(hostPort string, attempt int) error {
	conn, err := dialUTLS(context.Background(), "tcp", hostPort)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// ProbeHandshakeAuto dials with plain HelloChrome_Auto (no captured spec) —
// A/B against the captured-spec path. Debug helper.
func ProbeHandshakeAuto(hostPort string) error {
	d := net.Dialer{}
	conn, err := d.DialContext(context.Background(), "tcp", hostPort)
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(hostPort)
	tlsConn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloChrome_Auto)
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		conn.Close()
		return err
	}
	tlsConn.Close()
	return nil
}

// ProbeHandshakeNoMLKEM dials with the captured spec but replaces the
// key_share extension with a freshly generated x25519-only share — tests
// whether the replayed ML-KEM share is what the peer rejects.
func ProbeHandshakeNoMLKEM(hostPort string) error {
	spec := &utls.ClientHelloSpec{}
	if err := spec.FromRaw(chrome150HelloRaw, true); err != nil {
		return err
	}
	// strip key_share; utls regenerates a fresh one from CurSettings? No —
	// instead drop ML-KEM by replacing KeyShareExtension entries.
	for _, e := range spec.Extensions {
		if ks, ok := e.(*utls.KeyShareExtension); ok {
			ks.KeyShares = []utls.KeyShare{{Group: utls.CurveID(0x001d)}}
		}
		if sg, ok := e.(*utls.SupportedCurvesExtension); ok {
			sg.Curves = []utls.CurveID{utls.X25519, utls.CurveP256, utls.CurveP384}
		}
	}
	d := net.Dialer{}
	conn, err := d.DialContext(context.Background(), "tcp", hostPort)
	if err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(hostPort)
	tlsConn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloCustom)
	if err := tlsConn.ApplyPreset(spec); err != nil {
		conn.Close()
		return err
	}
	if err := tlsConn.HandshakeContext(context.Background()); err != nil {
		conn.Close()
		return err
	}
	tlsConn.Close()
	return nil
}
