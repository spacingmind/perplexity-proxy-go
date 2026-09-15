package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	http2 "github.com/spacingmind/perplexity-proxy-go/internal/http2x"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// frameprobe points the production h2 stack (http2x fork + utls dial) at a
// local TLS sink for wire-frame diffing. Debug tool, not shipped surface.
func main() {
	http2.EnableChromeFingerprint()
	t := transport.NewDebugTransport("https://127.0.0.1:9444")
	req, _ := http.NewRequest("POST", "https://127.0.0.1:9444/rest/sse/perplexity_ask", strings.NewReader(`{"params":{},"query_str":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.Do(req)
	if err != nil {
		fmt.Println("err:", err)
		os.Exit(1)
	}
	fmt.Println("status:", resp.StatusCode)
	_ = time.Second
	_ = context.Background
	_ = net.Dialer{}
}
