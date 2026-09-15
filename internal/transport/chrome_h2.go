package transport

import (
	"context"
	"crypto/tls"
	"net"

	http2 "github.com/spacingmind/perplexity-proxy-go/internal/http2x"
)

// Chrome's HTTP/2 connection preamble, as established reference values:
//
//	SETTINGS  HEADER_TABLE_SIZE=65536, ENABLE_PUSH=0,
//	          MAX_CONCURRENT_STREAMS=1000, INITIAL_WINDOW_SIZE=6291456,
//	          MAX_HEADER_LIST_SIZE=262144
//	WINDOW_UPDATE (stream 0) -> 15663105
//	pseudo-header order :method :authority :scheme :path
//	PRIORITY frames for stream 0 and each new stream
//
// newChromeH2Transport configures an *http2.Transport as close to that
// shape as the public x/net/http2 API allows. Per knob:
//
//   - MaxDecoderHeaderTableSize=65536 -> SETTINGS_HEADER_TABLE_SIZE.
//     x/net default is 4096 and is omitted when equal to the initial
//     value; setting 65536 makes Chrome's value appear on the wire.
//   - MaxHeaderListSize=262144 -> SETTINGS_MAX_HEADER_LIST_SIZE.
//     x/net default sends 10 MiB, which Chrome never does.
//   - MaxReadFrameSize=16384 -> SETTINGS_MAX_FRAME_SIZE. Chrome does not
//     advertise this setting (it uses the 16384 default); x/net sends it
//     unconditionally, so the value is matched and only the setting's
//     presence remains as a delta.
//   - MaxEncoderHeaderTableSize=65536: encoder-side only, not visible on
//     the wire; set for symmetry, no fingerprint effect.
//
// NOT tunable through the public API (residual fingerprint deltas,
// candidates for a forked/vendored http2 if bot detection still trips):
//
//   - SETTINGS_INITIAL_WINDOW_SIZE: x/net sends MaxUploadBufferPerStream,
//     fixed at 4 MiB (4194304); Chrome sends 6291456.
//   - Connection WINDOW_UPDATE after the preface: x/net sends
//     MaxUploadBufferPerConnection, fixed at 1 GiB; Chrome sends
//     15663105.
//   - SETTINGS_MAX_CONCURRENT_STREAMS=1000: x/net omits it entirely.
//   - SETTINGS frame ordering: x/net order is ENABLE_PUSH,
//     INITIAL_WINDOW_SIZE, MAX_FRAME_SIZE, MAX_HEADER_LIST_SIZE,
//     HEADER_TABLE_SIZE; Chrome sends HEADER_TABLE_SIZE first.
//   - PRIORITY frames (stream 0 + per stream): x/net/http2 never sends
//     PRIORITY, which is itself a non-Chrome signal.
//   - Pseudo-header order: x/net emits :method :scheme :authority :path;
//     Chrome emits :method :authority :scheme :path.
//
// Regular header ordering is fine: Go writes header lists sorted
// alphabetically, matching Chrome's sorted lowercase emission.
func newChromeH2Transport(dialTLS func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error)) *http2.Transport {
	http2.EnableChromeFingerprint()
	return &http2.Transport{
		DialTLSContext:            dialTLS,
		MaxDecoderHeaderTableSize: 65536, // Chrome SETTINGS_HEADER_TABLE_SIZE
		MaxEncoderHeaderTableSize: 65536, // encoder side; no wire effect
		MaxHeaderListSize:         262144,
		MaxReadFrameSize:          16384, // spec default; Chrome omits the setting
	}
}
