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

// applyChromeHello configures conn with the captured spec. Falls back to
// doing nothing (caller already constructed the UConn with HelloChrome_Auto,
// which then remains in effect) when the capture cannot be applied.
func applyChromeHello(conn *utls.UConn) {
	if spec := chromeClientHelloSpec(); spec != nil {
		_ = conn.ApplyPreset(spec)
	}
}
