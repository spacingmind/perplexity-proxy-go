package transport

import (
	_ "embed"
	"sync"

	utls "github.com/refraction-networking/utls"
)

//go:embed data/chrome150_clienthello.bin
var chrome150HelloRaw []byte

var (
	helloOnce sync.Once
	helloSpec *utls.ClientHelloSpec
	helloErr  error
)

// chromeClientHelloSpec parses the captured curl_cffi impersonate=chrome
// (Chrome 150) ClientHello record. utls's newest builtin is Chrome 133,
// which bot scoring can distinguish from the Chrome 150 the User-Agent
// claims. Returns nil on a malformed capture; callers fall back to
// HelloChrome_Auto.
func chromeClientHelloSpec() *utls.ClientHelloSpec {
	helloOnce.Do(func() {
		spec := &utls.ClientHelloSpec{}
		if err := spec.FromRaw(chrome150HelloRaw, true); err != nil {
			helloErr = err
			return
		}
		helloSpec = spec
	})
	return helloSpec
}

// applyChromeHello configures conn with the captured spec. The UConn must
// be constructed with utls.HelloCustom — utls regenerates the handshake for
// known IDs like HelloChrome_Auto during HandshakeContext, silently
// overwriting any preset applied earlier. Falls back to HelloChrome_Auto's
// spec when the capture cannot be applied.
func applyChromeHello(conn *utls.UConn) {
	if spec := chromeClientHelloSpec(); spec != nil {
		if err := conn.ApplyPreset(spec); err == nil {
			return
		}
	}
	fallback, err := utls.UTLSIdToSpec(utls.HelloChrome_Auto)
	if err == nil {
		_ = conn.ApplyPreset(&fallback)
	}
}
