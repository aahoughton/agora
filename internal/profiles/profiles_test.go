package profiles

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestProfiles(t *testing.T) {
	if got := strings.Join(Names(), ","); got != "antigravity,claude,codex,pi" {
		t.Fatalf("profiles = %s", got)
	}
	if _, err := Get("gemini"); err == nil {
		t.Fatal("unknown profile accepted")
	}
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			p, _ := Get(name)
			var b strings.Builder
			b.WriteString("# " + p.Description + "\n# skills: " + p.SkillsDir + "\n\n")
			for _, wait := range []time.Duration{50 * time.Second, 4 * time.Minute} {
				out, err := p.MCPOperation("http://127.0.0.1:8787/mcp?namespace=default", wait, "")
				if err != nil {
					t.Fatal(err)
				}
				b.WriteString("## client config, server waits up to " + wait.String() + "\n" + out.Instructions() + "\n")
			}
			out, err := p.MCPOperation("http://127.0.0.1:8787/mcp?namespace=default", 4*time.Minute, "AGORA_TOKEN")
			if err != nil {
				t.Fatal(err)
			}
			b.WriteString("## client config with a token, server waits up to 4m0s\n" + out.Instructions() + "\n")
			if p.HookContract != "" {
				hook, err := p.HookOperation("agora hook stop")
				if err != nil {
					t.Fatal(err)
				}
				b.WriteString("## stop hook\n" + hook.Instructions())
			}
			golden(t, name, b.String())
		})
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	got = strings.TrimRight(got, "\n") + "\n"
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs; run go test ./internal/profiles -update and review the diff\n%s", path, got)
	}
}

func TestTimeoutsOnlyWhenNeeded(t *testing.T) {
	p, _ := Get("codex")
	short, _ := p.MCPOperation("u", 50*time.Second, "")
	long, _ := p.MCPOperation("u", 4*time.Minute, "")
	if strings.Contains(short.Value, "tool_timeout_sec") || !strings.Contains(long.Value, "tool_timeout_sec = 250") {
		t.Fatalf("short:\n%s\nlong:\n%s", short.Value, long.Value)
	}
}

func TestLoadRejectsBadProfiles(t *testing.T) {
	raw, err := files.ReadFile("text/claude.md")
	if err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"missing":              "## description\nx\n",
		"op":                   strings.Replace(string(raw), `"op":"command"`, `"op":"guess"`, 1),
		"contract":             strings.Replace(string(raw), "## hook_contract\nclaude", "## hook_contract\ngeneric", 1),
		"field":                strings.Replace(string(raw), `"file":`, `"typo":`, 1),
		"section":              string(raw) + "\n## typo\nx\n",
		"missing hook":         strings.Replace(string(raw), "## hook_contract\nclaude", "", 1),
		"missing override env": strings.Replace(string(raw), "## home_env\nCLAUDE_CONFIG_DIR", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(fstest.MapFS{"text/x.md": {Data: []byte(source)}}); err == nil {
				t.Fatal("accepted invalid profile")
			}
		})
	}
}

func TestQuoting(t *testing.T) {
	const url = `http://h/mcp?namespace=x&x=';printf%20pwned;'"q\`
	const command = `agora hook stop --url "http://h/"`
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			p, _ := Get(name)
			op, err := p.MCPOperation(url, 4*time.Minute, "")
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if op.Op == "toml-table" {
				for line := range strings.Lines(op.Value) {
					if value, ok := strings.CutPrefix(strings.TrimSpace(line), "url = "); ok {
						if err := json.Unmarshal([]byte(value), &got); err != nil {
							t.Fatal(err)
						}
					}
				}
			} else {
				var object map[string]any
				if err := json.Unmarshal([]byte(op.Value), &object); err != nil {
					t.Fatal(err)
				}
				got, _ = object["url"].(string)
				if op.Op == "command" && op.Run[len(op.Run)-1] != strings.TrimSpace(op.Value) {
					t.Fatal("argv changed JSON")
				}
			}
			if got != url {
				t.Fatalf("url = %q", got)
			}
			if p.HookContract == "" {
				return
			}
			hook, err := p.HookOperation(command)
			if err != nil {
				t.Fatal(err)
			}
			want := command
			if p.HookContract == "antigravity" {
				want += " --client antigravity"
			}
			value, err := jsonValue([]byte(hook.Value))
			if err != nil || !hasCommand(value, want) {
				t.Fatalf("hook = %s, %v", hook.Value, err)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			p, _ := Get(name)
			env := map[string]string{"HOME": t.TempDir()}
			getenv := func(key string) string { return env[key] }
			op, _ := p.MCPOperation("http://h/mcp?namespace=default", time.Minute, "")
			normal, err := p.ResolveOperation(op, getenv)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(normal.File, env["HOME"]+"/") {
				t.Fatal(normal.File)
			}
			if p.HomeEnv == "" {
				return
			}
			override := t.TempDir()
			env[p.HomeEnv] = override
			got, err := p.ResolveOperation(op, getenv)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(got.File, override+"/") {
				t.Fatal(got.File)
			}
			skills, err := p.Resolve(p.SkillsDir, getenv)
			if err != nil {
				t.Fatal(err)
			}
			root := override
			if name == "codex" || name == "pi" {
				root = env["HOME"]
			}
			if !strings.HasPrefix(skills, root+"/") {
				t.Fatal(skills)
			}
			env[p.HomeEnv] = "relative"
			if _, err := p.ResolveOperation(op, getenv); err == nil {
				t.Fatal("accepted relative override")
			}
		})
	}
}
