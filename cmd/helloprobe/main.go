package main

import (
	"fmt"
	"os"

	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// helloprobe: perform the production TLS dial against an arbitrary host:port
// and dump the ClientHello bytes we actually send (debug tool).
func main() {
	err := transport.ProbeHandshake("127.0.0.1:8443", os.Getenv("PROBE_OUT"))
	fmt.Println("probe:", err)
}
