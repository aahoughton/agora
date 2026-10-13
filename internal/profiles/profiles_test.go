package profiles

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestProfiles(t *testing.T) {
	if got := strings.Join(Names(), ","); got != "claude,codex" {
		t.Fatalf("profiles = %s", got)
	}
	if _, err := Get("gemini"); err == nil || !strings.Contains(err.Error(), "claude, codex") {
		t.Fatalf("unknown profile err = %v", err)
	}
	const url = "http://127.0.0.1:8787/mcp?namespace=default"
	for _, name := range Names() {
		p, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		b.WriteString("# " + p.Description + "\n# skills: " + p.SkillsDir + "\n\n")
		for _, wait := range []time.Duration{50 * time.Second, 4 * time.Minute} {
			out, err := p.MCPConfig(url, wait, "")
			if err != nil {
				t.Fatal(err)
			}
			b.WriteString("## mcp-config, server waits up to " + wait.String() + "\n" + out + "\n")
		}
		out, err := p.MCPConfig(url, 4*time.Minute, "AGORA_TOKEN")
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("## mcp-config with a token, server waits up to 4m0s\n" + out + "\n")
		hook, err := p.StopHook("agora hook stop")
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("## stop hook\n" + hook)
		golden(t, name, b.String())
	}
}

func TestTimeoutsOnlyWhenNeeded(t *testing.T) {
	p, err := Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	short, _ := p.MCPConfig("u", 50*time.Second, "")
	long, _ := p.MCPConfig("u", 4*time.Minute, "")
	if strings.Contains(short, "tool_timeout_sec") || !strings.Contains(long, "tool_timeout_sec = 250") {
		t.Fatalf("short:\n%s\nlong:\n%s", short, long)
	}
}

func TestLoadRejectsBadProfiles(t *testing.T) {
	_, err := load(fstest.MapFS{"text/x.md": {Data: []byte("## description\nx\n")}})
	if err == nil || !strings.Contains(err.Error(), "missing skills_dir") {
		t.Fatalf("err = %v", err)
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/profiles -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s (run go test ./internal/profiles -update and review the diff):\n%s", name, path, got)
	}
}

// TestQuoting checks that a URL or command with quotes survives into each
// printed setup unchanged: as one shell word, and as JSON or TOML strings.
func TestQuoting(t *testing.T) {
	const url = `http://h/m?x=';printf pwned;'"q\`
	const command = `agora hook stop --url "http://h/"`
	for _, name := range Names() {
		p, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		out, err := p.MCPConfig(url, 4*time.Minute, "")
		if err != nil {
			t.Fatal(err)
		}
		var urls []string
		for line := range strings.Lines(out) {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "claude mcp add "):
				args := strings.TrimPrefix(line, "claude mcp add --transport http --scope user agora ")
				got, err := exec.CommandContext(t.Context(), "sh", "-c", "printf '%s' "+args).Output()
				if err != nil {
					t.Fatal(err)
				}
				urls = append(urls, string(got))
			case strings.HasPrefix(line, `"url": `), strings.HasPrefix(line, "url = "):
				raw := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(line, `"url": `), "url = "), ",")
				var s string
				if err := json.Unmarshal([]byte(raw), &s); err != nil {
					t.Fatalf("%s: %q: %v", name, line, err)
				}
				urls = append(urls, s)
			}
		}
		if len(urls) == 0 {
			t.Fatalf("%s: no URL found in:\n%s", name, out)
		}
		for _, u := range urls {
			if u != url {
				t.Errorf("%s: url = %q, want %q", name, u, url)
			}
		}

		hook, err := p.StopHook(command)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Hooks struct {
				Stop []struct{ Hooks []struct{ Command string } }
			}
		}
		_, js, ok := strings.Cut(hook, "{")
		if !ok {
			t.Fatalf("%s: no JSON in hook:\n%s", name, hook)
		}
		if err := json.Unmarshal([]byte("{"+js), &cfg); err != nil || cfg.Hooks.Stop[0].Hooks[0].Command != command {
			t.Errorf("%s: hook = %+v, %v\n%s", name, cfg, err, hook)
		}
	}
}
