package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spacingmind/perplexity-proxy-go/internal/pplx"
)

// cmdDump is a temporary live-debug command: runs a real ask and writes every
// raw SSE line to /tmp/pplx-sse-dump.txt. Not part of the stable surface.
func cmdDump(args []string) error {
	sp, err := loadSpec()
	if err != nil {
		return err
	}
	if _, err := loadToken(); err != nil {
		return err
	}
	t, err := newTransport(sp)
	if err != nil {
		return err
	}
	query := "what is the capital of Vietnam"
	if len(args) > 0 {
		query = args[0]
	}
	conv := pplx.NewConversation(t, sp)
	f, err := os.Create("/tmp/pplx-sse-dump.txt")
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	start := time.Now()
	err = conv.AskRaw(ctx, query, pplx.AskOptions{}, func(line []byte) {
		w.Write(line)
		w.WriteByte('\n')
	})
	w.WriteString(fmt.Sprintf("\n# elapsed=%s err=%v\n", time.Since(start), err))
	fmt.Println("dumped to /tmp/pplx-sse-dump.txt")
	return err
}
