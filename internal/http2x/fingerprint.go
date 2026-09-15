// Chrome wire-shape toggles for this fork. Disabled keeps byte-identical
// upstream behavior.
package http2

// ChromeFingerprint carries the wire-shape toggles.
type ChromeFingerprint struct {
	// Enabled switches the connection preface and per-stream framing to
	// Chrome's byte shapes: SETTINGS set/order, stream-0 PRIORITY,
	// connection window 15663105, per-stream PRIORITY before HEADERS.
	Enabled bool
}

var chromeFingerprint = ChromeFingerprint{Enabled: false}

// SetChromeFingerprint enables/disables Chrome wire shaping. Must be
// called before any connection is created; affects new connections only.
func SetChromeFingerprint(cf ChromeFingerprint) { chromeFingerprint = cf }

// EnableChromeFingerprint turns on all Chrome wire shapes.
func EnableChromeFingerprint() { chromeFingerprint = ChromeFingerprint{Enabled: true} }
