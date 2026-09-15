package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spacingmind/perplexity-proxy-go/internal/pplx"
	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
)

func main() {
	sp, _ := spec.Load()
	m := sp.Model(os.Args[1])
	fmt.Printf("model lookup %q -> identifier=%q mode=%q\n", os.Args[1], m.Identifier, m.Mode)
	conv := pplx.NewConversation(nil, sp)
	ans, err := conv.BridgeProbe(context.Background(), "2+2? number only", m.Identifier)
	if ans != nil {
		fmt.Printf("answer=%q citations=%d err=%v\n", ans.Text, len(ans.Citations), err)
	} else {
		fmt.Printf("ans=nil err=%v\n", err)
	}
	raw, rerr := pplx.BridgeRaw(context.Background(), "what is the capital of France? one word", "experimental")
	fmt.Printf("RAW err=%v: %.400s\n", rerr, raw)
}

func init() { _ = os.Args }
