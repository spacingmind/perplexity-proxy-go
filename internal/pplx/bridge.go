package pplx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Bridge: when the Go transport's TLS/H2 fingerprint is being blocked by the
// server's bot scoring, shell out to the reference Python client (curl_cffi
// engine), which passes consistently. Enabled via PPLX_BRIDGE=1 or auto when
// a Go ask fails with fraud/rate-limit signals.

const bridgeScriptDefault = "scripts/bridge_ask.py"

// bridgePython resolves the python to use: PPLX_PYTHON env, else the
// managed bridge venv if present, else python3 from PATH.
func bridgePython() string {
	if v := os.Getenv("PPLX_PYTHON"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err == nil {
		cand := filepath.Join(home, ".pplx", "bridge-venv", "bin", "python")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "python3"
}

func bridgeEnabled() bool {
	return os.Getenv("PPLX_BRIDGE") == "1"
}

// bridgeAuto: automatic fallback on fingerprint-ish errors. Off in tests
// (PPLX_NO_BRIDGE=1) so fake transports surface their errors.
func bridgeAuto() bool {
	return os.Getenv("PPLX_NO_BRIDGE") != "1" && os.Getenv("PPLX_BRIDGE") != "0"
}

// askViaBridge runs scripts/bridge_ask.py (python from PPLX_PYTHON, default
// python3) with the query, mirroring Conversation.Ask's contract including
// followup state.
func (c *Conversation) askViaBridge(ctx context.Context, query string, opt AskOptions) (*Answer, error) {
	py := bridgePython()
	script := os.Getenv("PPLX_BRIDGE_SCRIPT")
	if script == "" {
		script = bridgeScriptDefault
	}
	req := map[string]any{"query": query}
	if m := c.sp.Model(opt.Model); m.Identifier != "" {
		req["model"] = m.Identifier
	}
	if opt.SourceFocus != "" {
		req["source_focus"] = opt.SourceFocus
	}
	if c.backendUUID != "" {
		req["backend_uuid"] = c.backendUUID
		if c.readWriteToken != "" {
			req["read_write_token"] = c.readWriteToken
		}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, py, script)
	cmd.Stdin = newByteReader(payload)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("bridge: %w (stdout: %.300s)", err, out)
	}
	if os.Getenv("PPLX_DEBUG_BRIDGE") == "1" {
		fmt.Fprintf(os.Stderr, "bridge stdout: %.500s\n", out)
	}
	var res struct {
		Answer         string     `json:"answer"`
		Citations      []Citation `json:"citations"`
		ThreadTitle    string     `json:"thread_title"`
		BackendUUID    string     `json:"backend_uuid"`
		ReadWriteToken string     `json:"read_write_token"`
		Error          string     `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("bridge decode: %w (out: %.200s)", err, out)
	}
	if res.Error != "" {
		return nil, fmt.Errorf("bridge: %s", res.Error)
	}
	if res.BackendUUID != "" {
		c.backendUUID = res.BackendUUID
	}
	if res.ReadWriteToken != "" {
		c.readWriteToken = res.ReadWriteToken
	}
	return &Answer{
		Text:        res.Answer,
		Citations:   res.Citations,
		ThreadTitle: res.ThreadTitle,
		UUID:        c.backendUUID,
	}, nil
}

type byteReader struct {
	*bytes.Reader
}

func newByteReader(b []byte) *byteReader { return &byteReader{Reader: bytes.NewReader(b)} }

// BridgeProbe exposes askViaBridge for debugging (cmd/bridgeprobe).
func (c *Conversation) BridgeProbe(ctx context.Context, query, modelIdent string) (*Answer, error) {
	opt := AskOptions{Model: modelIdent}
	return c.askViaBridge(ctx, query, opt)
}

// BridgeRaw runs the bridge script and returns its raw stdout (debug).
func BridgeRaw(ctx context.Context, query, modelIdent string) (string, error) {
	py := os.Getenv("PPLX_PYTHON")
	if py == "" {
		py = "python3"
	}
	cmd := exec.CommandContext(ctx, py, bridgeScriptDefault, "/tmp/bridge_raw_out.json")
	if err := cmd.Run(); err != nil {
		return "", err
	}
	b, err := os.ReadFile("/tmp/bridge_raw_out.json")
	return string(b), err
}
