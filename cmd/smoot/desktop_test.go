//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// TestDesktopSetupWithoutEnvironment installs Codex with --with-token-file,
// then runs the MCP bridge and Stop hook its config names with only what a
// Dock-launched app has: HOME and a minimal PATH.
func TestDesktopSetupWithoutEnvironment(t *testing.T) {
	url, token, _ := tokenServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	// Not the default path, so only the configured --token-file finds it.
	tokenFile := filepath.Join(home, "secrets", "smoot-token")
	if err := os.MkdirAll(filepath.Dir(tokenFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	smoot := filepath.Join(t.TempDir(), "smoot")
	if err := os.Symlink(self, smoot); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t.Context(), "", "client", "install", "codex", "--url", url, "--namespace", "default",
		"--with-token-file", "--token-file", tokenFile, "--smoot-path", smoot, "--stop-hook", "--agent", "codex-desk")
	if r.code != exitOK {
		t.Fatalf("install: %+v", r)
	}
	appEnv := []string{"HOME=" + home, "PATH=/usr/bin:/bin"}

	var config struct {
		MCPServers map[string]struct {
			Command string   `toml:"command"`
			Args    []string `toml:"args"`
		} `toml:"mcp_servers"`
	}
	raw, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	entry := config.MCPServers["smoot"]
	// The deadline kills a bridge that stops answering, which ends Scan.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	bridge := exec.CommandContext(ctx, entry.Command, entry.Args...)
	bridge.Env = appEnv
	var stderr bytes.Buffer
	bridge.Stderr = &stderr
	in, err := bridge.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := bridge.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	// fail stops the bridge before reading stderr, which os/exec writes
	// from its own goroutine until Wait returns.
	fail := func(format string, args ...any) {
		t.Helper()
		_ = bridge.Process.Kill()
		_ = bridge.Wait()
		t.Fatalf(format+"; stderr: %s", append(args, stderr.String())...)
	}
	lines := bufio.NewScanner(out)
	call := func(msg string) map[string]any {
		t.Helper()
		if _, err := in.Write([]byte(msg + "\n")); err != nil {
			fail("write: %v", err)
		}
		if !strings.Contains(msg, `"id"`) {
			return nil
		}
		if !lines.Scan() {
			fail("bridge closed: %v", lines.Err())
		}
		var resp map[string]any
		if err := json.Unmarshal(lines.Bytes(), &resp); err != nil {
			fail("response: %v", err)
		}
		return resp
	}
	call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	call(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	resp := call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"start","arguments":{"conversation":"desk","agent":"codex-desk","topic":"t","end":"agreement"}}}`)
	if got, _ := json.Marshal(resp); !strings.Contains(string(got), "you post as account root") {
		fail("start through the bridge: %s", got)
	}
	_ = in.Close()
	if err := bridge.Wait(); err != nil {
		t.Fatalf("bridge: %v; stderr: %s", err, stderr.String())
	}

	var hooks struct {
		Hooks struct {
			Stop []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"Stop"`
		} `json:"hooks"`
	}
	raw, err = os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		t.Fatal(err)
	}
	hook := exec.CommandContext(t.Context(), "/bin/sh", "-c", hooks.Hooks.Stop[0].Hooks[0].Command)
	hook.Env = appEnv
	hook.Stdin = strings.NewReader(`{"session_id":"s1","stop_hook_active":false,"hook_event_name":"Stop"}`)
	got, err := hook.CombinedOutput()
	if err != nil || !strings.Contains(string(got), `"decision":"block"`) {
		t.Fatalf("hook = %s, %v", got, err)
	}
}
