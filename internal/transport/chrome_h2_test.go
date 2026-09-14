package transport

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
)

func TestChromeH2Transport_ConfigValues(t *testing.T) {
	dial := func(context.Context, string, string, *tls.Config) (net.Conn, error) { return nil, nil }
	tr := newChromeH2Transport(dial)

	if tr.MaxDecoderHeaderTableSize != 65536 {
		t.Errorf("MaxDecoderHeaderTableSize = %d, want 65536 (Chrome SETTINGS_HEADER_TABLE_SIZE)", tr.MaxDecoderHeaderTableSize)
	}
	if tr.MaxEncoderHeaderTableSize != 65536 {
		t.Errorf("MaxEncoderHeaderTableSize = %d, want 65536", tr.MaxEncoderHeaderTableSize)
	}
	if tr.MaxHeaderListSize != 262144 {
		t.Errorf("MaxHeaderListSize = %d, want 262144 (Chrome SETTINGS_MAX_HEADER_LIST_SIZE)", tr.MaxHeaderListSize)
	}
	if tr.MaxReadFrameSize != 16384 {
		t.Errorf("MaxReadFrameSize = %d, want 16384 (spec default)", tr.MaxReadFrameSize)
	}
	if tr.DialTLSContext == nil {
		t.Error("DialTLSContext must be set (connection reuse via ALPN)")
	}
}
