package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aahoughton/smoot/internal/wording"
)

func TestClientInstall(t *testing.T) {
	for _, name := range []string{"codex", "antigravity", "pi"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", "")
			t.Setenv("PI_CODING_AGENT_DIR", "")
			args := []string{"client", "install", name, "--url", "http://127.0.0.1:9100", "--namespace", "team", "--with-token"}
			if name != "pi" {
				args = append(args, "--stop-hook")
			}
			r := runCLI(t.Context(), "", append(args, "--dry-run")...)
			if r.code != exitOK || !strings.Contains(r.stdout, "would write") {
				t.Fatalf("dry run: %+v", r)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("dry run created files: %v, %v", entries, err)
			}
			r = runCLI(t.Context(), "", args...)
			if r.code != exitOK || !strings.Contains(r.stdout, "wrote") {
				t.Fatalf("install: %+v", r)
			}
			if r = runCLI(t.Context(), "", args...); r.code != exitOK || strings.Contains(r.stdout, "wrote") {
				t.Fatalf("second install: %+v", r)
			}
			configPath := map[string]string{"codex": ".codex/config.toml", "antigravity": ".gemini/config/mcp_config.json", "pi": ".pi/agent/mcp.json"}[name]
			raw, err := os.ReadFile(filepath.Join(home, configPath))
			if err != nil || !strings.Contains(string(raw), "http://127.0.0.1:9100") || !strings.Contains(string(raw), "team") {
				t.Fatalf("config: %s, %v", raw, err)
			}
		})
	}
}

func TestClientInstallUnsupportedHookWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	for _, action := range []string{"config", "install"} {
		r := runCLI(t.Context(), "", "client", action, "pi", "--stop-hook")
		if r.code != exitError || r.stdout != "" || !strings.Contains(r.stderr, "no supported stop hook") {
			t.Fatalf("%s: %+v", action, r)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsupported hook wrote files: %v, %v", entries, err)
	}
}

func TestClientInstallSharedHookConflictPreventsAllWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	hookPath := filepath.Join(home, ".codex", "hooks.json")
	before := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"},{"type":"command","command":"smoot hook stop --url old"}]}]}}`
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t.Context(), "", "client", "install", "codex", "--stop-hook", "--force")
	if r.code != exitError || !strings.Contains(r.stderr, "shared or customized hook group") {
		t.Fatalf("shared hook: %+v", r)
	}
	got, err := os.ReadFile(hookPath)
	if err != nil || string(got) != before {
		t.Fatalf("hook changed: %s, %v", got, err)
	}
	for _, path := range []string{".codex/config.toml", ".codex/hooks.json.smoot-bak", ".agents/skills/smoot/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(home, path)); !os.IsNotExist(err) {
			t.Fatalf("created %s: %v", path, err)
		}
	}
}

func TestClientInstallForceReplacesOwnHookFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	hookPath := filepath.Join(home, ".codex", "hooks.json")
	before := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"smoot hook stop --url old","timeout":5}]}]}}`
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := runCLI(t.Context(), "", "client", "install", "codex", "--stop-hook", "--force"); r.code != exitOK {
		t.Fatalf("install: %+v", r)
	}
	got, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(got); !strings.Contains(s, `"statusMessage"`) || strings.Contains(s, "--url old") || strings.Contains(s, `"timeout"`) {
		t.Fatalf("hook not replaced: %s", got)
	}
}

func TestClientInstallApprovalsAreOptIn(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	args := []string{"client", "install", "antigravity", "--stop-hook"}
	install := func(extra ...string) cliResult {
		return runCLIInput(t.Context(), "http://127.0.0.1:9100", "", env, append(append([]string{}, args...), extra...)...)
	}
	if r := install(); r.code != exitOK {
		t.Fatalf("default install: %+v", r)
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("approvals written by default: %v", err)
	}
	if r := install("--approve-tools", "--dry-run"); r.code != exitOK {
		t.Fatalf("approval dry run: %+v", r)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("approval dry run wrote a file: %v", err)
	}
	if r := install("--approve-tools"); r.code != exitOK {
		t.Fatalf("approve: %+v", r)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct{ Permissions struct{ Allow []string } }
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	names := wording.Default().Tools()
	if len(cfg.Permissions.Allow) != len(names) {
		t.Fatalf("approvals = %v", cfg.Permissions.Allow)
	}
	for i, name := range names {
		if cfg.Permissions.Allow[i] != "mcp(smoot/"+name+")" {
			t.Fatalf("missing registered tool %s", name)
		}
	}
}

func TestUnsupportedApprovalsWriteNothing(t *testing.T) {
	for _, name := range []string{"codex", "pi"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			env := map[string]string{"HOME": home}
			for _, action := range []string{"config", "install"} {
				r := runCLIInput(t.Context(), "http://127.0.0.1:9100", "", env, "client", action, name, "--approve-tools")
				if r.code != exitError || r.stdout != "" || !strings.Contains(r.stderr, "no separate tool approval setup") {
					t.Fatalf("unsupported approvals: %+v", r)
				}
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unsupported approvals wrote files: %v %v", entries, err)
			}
		})
	}
}

func TestClientSkillsRefusesSymlink(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "notes.md")
	link := filepath.Join(home, ".agents", "skills", "smoot", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("keep these notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r := runCLIInput(t.Context(), "", "", map[string]string{"HOME": home}, "client", "skills", "pi", "--force")
	if r.code != exitError || !strings.Contains(r.stderr, link+" -> "+target) {
		t.Fatalf("symlink accepted: %+v", r)
	}
	if !strings.Contains(r.stderr, "smoot client skills --dir DIR") || !strings.Contains(r.stderr, "replace the link with a regular file") {
		t.Fatalf("missing symlink advice: %+v", r)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "keep these notes" {
		t.Fatalf("symlink target changed: %s %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "convene")); !os.IsNotExist(err) {
		t.Fatalf("partial skill install: %v", err)
	}
}

func TestClientInstallSymlinkGuidance(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "config.toml")
	link := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r := runCLIInput(t.Context(), "http://127.0.0.1:9100", "", map[string]string{"HOME": home}, "client", "install", "codex", "--namespace", "team", "--with-token", "--stop-hook", "--max-wait", "4m", "--force")
	for _, want := range []string{link + " -> " + target, "smoot client config codex --url http://127.0.0.1:9100 --namespace team --max-wait 4m0s --with-token --stop-hook", "apply the printed setup", "replace the link with a regular file"} {
		if !strings.Contains(r.stderr, want) {
			t.Fatalf("missing %q: %+v", want, r)
		}
	}
	if r.code != exitError || strings.Contains(r.stderr, "--force") {
		t.Fatalf("invalid manual command: %+v", r)
	}
}
