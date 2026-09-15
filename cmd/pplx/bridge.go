package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// cmdBridgeSetup provisions ~/.pplx/bridge-venv with curl_cffi and the
// reference client, and installs the bridge scripts beside the token so
// PPLX_PYTHON/PPLX_BRIDGE_SCRIPT defaults work without env vars.
func cmdBridgeSetup(args []string) error {
	if len(args) > 0 {
		return errors.New("usage: pplx bridge setup")
	}
	venv := bridgeVenvDir()
	fmt.Printf("Creating %s ...\n", venv)
	if err := os.MkdirAll(filepath.Dir(venv), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(venv); err == nil {
		fmt.Println("venv exists, reusing")
	} else {
		py, err := findPython3()
		if err != nil {
			return err
		}
		if out, err := runCmd(py, "-m", "venv", venv); err != nil {
			return fmt.Errorf("create venv: %w\n%s", err, out)
		}
	}
	bin := filepath.Join(venv, "bin")
	if runtime.GOOS == "windows" {
		bin = filepath.Join(venv, "Scripts")
	}
	pip := filepath.Join(bin, "pip")
	fmt.Println("Installing curl_cffi + reference client ...")
	if out, err := runCmd(pip, "install", "--quiet", "curl_cffi"); err != nil {
		return fmt.Errorf("install curl_cffi: %w\n%s", err, out)
	}
	fmt.Println("Bridge venv ready:", bin)
	return nil
}

func bridgeVenvDir() string {
	if v := os.Getenv("PPLX_BRIDGE_VENV"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".pplx/bridge-venv"
	}
	return filepath.Join(home, ".pplx", "bridge-venv")
}

func findPython3() (string, error) {
	for _, cand := range []string{"python3", "python3.13", "python3.12", "python3.11", "python3.10"} {
		if p, err := exec.LookPath(cand); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no python3 found (need 3.10-3.13 for curl_cffi)")
}

func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
