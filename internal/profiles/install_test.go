package profiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aahoughton/smoot/internal/wording"
	"github.com/aahoughton/smoot/skills"
)

// The external client fixture exercises argv execution and config side effects.
// Real client acceptance is checked separately in the spike's smoke runs.
func TestClientCommandHelper(_ *testing.T) {
	if os.Getenv("SMOOT_INSTALL_TEST_HELPER") != "1" {
		return
	}
	// Claude treats a present-but-empty override differently from an unset one.
	if value, present := os.LookupEnv("CLAUDE_CONFIG_DIR"); present && value == "" {
		os.Exit(10)
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 6 {
		os.Exit(2)
	}
	args = args[1:]
	if args[0] != "mcp" || args[2] != "--scope" || args[3] != "user" || args[4] != "smoot" {
		os.Exit(3)
	}
	path := filepath.Join(os.Getenv("HOME"), ".claude.json")
	if root := os.Getenv("CLAUDE_CONFIG_DIR"); root != "" {
		path = filepath.Join(root, ".claude.json")
	}
	root := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &root); err != nil {
			os.Exit(4)
		}
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		root["mcpServers"] = servers
	}
	switch args[1] {
	case "remove":
		delete(servers, "smoot")
	case "add-json":
		if _, ok := servers["smoot"]; ok {
			os.Exit(5)
		}
		var entry any
		if len(args) != 6 || json.Unmarshal([]byte(args[5]), &entry) != nil {
			os.Exit(6)
		}
		servers["smoot"] = entry
	default:
		os.Exit(7)
	}
	raw, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		os.Exit(8)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		os.Exit(9)
	}
	os.Exit(0)
}

func clientFixture(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec " + shellQuote(executable) + " -test.run=^TestClientCommandHelper$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SMOOT_INSTALL_TEST_HELPER", "1")
}

func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range Names() {
		p, _ := Get(name)
		if p.HomeEnv != "" {
			t.Setenv(p.HomeEnv, "")
		}
	}
	t.Setenv("SMOOT_TOKEN", "synthetic-secret-must-not-appear")
	return home
}

func operations(t *testing.T, p *Profile, url, token string) ([]Operation, string) {
	t.Helper()
	setup := Setup{MCPURL: url, MaxWait: 4 * time.Minute, TokenEnv: token, ApproveTools: p.Name == "claude" || p.Name == "antigravity", ToolNames: wording.Default().Tools()}
	if p.HookContract != "" {
		setup.HookCommand = "smoot hook stop --url http://127.0.0.1:8790 --namespace team"
	}
	ops, err := p.Operations(setup, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := p.Resolve(p.SkillsDir, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	return ops, dir
}

func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[strings.TrimPrefix(path, dir+string(os.PathSeparator))] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInstallProfiles(t *testing.T) {
	clientFixture(t)
	for _, name := range Names() {
		for _, token := range []string{"", "SMOOT_TOKEN"} {
			t.Run(name+"/"+token, func(t *testing.T) {
				home := isolatedHome(t)
				p, _ := Get(name)
				ops, dir := operations(t, p, "http://127.0.0.1:8790/mcp?namespace=team", token)
				install := func(dry bool) string {
					t.Helper()
					plan, err := Prepare(ops, skills.FS, dir, false)
					if err != nil {
						t.Fatal(err)
					}
					var out bytes.Buffer
					if err := plan.Apply(t.Context(), dry, &out); err != nil {
						t.Fatal(err)
					}
					return out.String()
				}
				if out := install(true); !strings.Contains(out, "would write") || len(tree(t, home)) != 0 {
					t.Fatal("dry-run wrote files")
				}
				install(false)
				first := tree(t, home)
				if out := install(false); strings.Contains(out, "wrote") {
					t.Fatal(out)
				}
				if !reflect.DeepEqual(first, tree(t, home)) {
					t.Fatal("second install changed bytes")
				}
				for path, raw := range first {
					if strings.Contains(raw, os.Getenv("SMOOT_TOKEN")) {
						t.Fatalf("token leaked to %s", path)
					}
				}
				var config strings.Builder
				seen := map[string]bool{}
				for _, op := range ops {
					if seen[op.File] {
						continue
					}
					seen[op.File] = true
					rel, _ := filepath.Rel(home, op.File)
					fmt.Fprintf(&config, "## %s\n%s\n", rel, first[rel])
				}
				suffix := ""
				if token != "" {
					suffix = "-token"
				}
				golden(t, name+"-installed"+suffix, config.String())
				for _, skill := range []string{"smoot", "convene"} {
					if _, err := os.Stat(filepath.Join(dir, skill, "SKILL.md")); err != nil {
						t.Fatal(err)
					}
				}
				changed, _ := operations(t, p, "http://127.0.0.1:9999/mcp?namespace=other", token)
				if _, err := Prepare(changed, skills.FS, dir, false); err == nil {
					t.Fatal("conflict accepted")
				}
				if !reflect.DeepEqual(first, tree(t, home)) {
					t.Fatal("conflict wrote files")
				}
				plan, err := Prepare(changed, skills.FS, dir, true)
				if err != nil {
					t.Fatal(err)
				}
				if err := plan.Apply(t.Context(), false, &bytes.Buffer{}); err != nil {
					t.Fatal(err)
				}
				backup, err := os.ReadFile(ops[0].File + ".smoot-bak")
				rel, _ := filepath.Rel(home, ops[0].File)
				if err != nil || string(backup) != first[rel] {
					t.Fatalf("backup: %s, %v", backup, err)
				}
				info, err := os.Stat(ops[0].File + ".smoot-bak")
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("backup permissions: %v, %v", info, err)
				}
			})
		}
	}
}

func TestInstallBesideUnrelated(t *testing.T) {
	clientFixture(t)
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			home := isolatedHome(t)
			p, _ := Get(name)
			ops, dir := operations(t, p, "http://127.0.0.1:8790/mcp?namespace=team", "")
			for _, op := range ops {
				seed := "{\n// keep this comment\n\"unrelated\": 17\n}\n"
				if op.Op == "command" {
					seed = "{\"unrelated\":17,\"mcpServers\":{\"other\":{\"url\":\"http://example.invalid\"}}}"
				}
				if op.Op == "toml-table" {
					seed = "# keep this comment\n[unrelated]\nvalue = 17\n"
				}
				if err := os.MkdirAll(filepath.Dir(op.File), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(op.File, []byte(seed), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := Prepare(ops, skills.FS, dir, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Apply(t.Context(), false, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			for _, op := range ops {
				raw, err := os.ReadFile(op.File)
				if err != nil || !bytes.Contains(raw, []byte("unrelated")) {
					t.Fatalf("lost unrelated value: %s %v", raw, err)
				}
				if op.Op != "command" && !bytes.Contains(raw, []byte("keep this comment")) {
					t.Fatal("lost comment")
				}
			}
			before := tree(t, home)
			plan, err = Prepare(ops, skills.FS, dir, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Apply(t.Context(), false, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, tree(t, home)) {
				t.Fatal("second install changed files")
			}
		})
	}
}

func TestInstallSkillConflictPreventsConfigWrites(t *testing.T) {
	for _, name := range []string{"codex", "pi", "antigravity"} {
		t.Run(name, func(t *testing.T) {
			home := isolatedHome(t)
			p, _ := Get(name)
			ops, dir := operations(t, p, "http://h/mcp?namespace=default", "")
			path := filepath.Join(dir, "smoot", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("custom"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := tree(t, home)
			if _, err := Prepare(ops, skills.FS, dir, false); err == nil {
				t.Fatal("accepted skill conflict")
			}
			if !reflect.DeepEqual(before, tree(t, home)) {
				t.Fatal("conflict changed files")
			}
		})
	}
}

func TestInstallRechecksBeforeWrite(t *testing.T) {
	home := isolatedHome(t)
	p, _ := Get("pi")
	ops, dir := operations(t, p, "http://h/mcp?namespace=default", "")
	plan, err := Prepare(ops, fstest.MapFS{}, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ops[0].File), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ops[0].File, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, home)
	if err := plan.Apply(t.Context(), false, &bytes.Buffer{}); err == nil {
		t.Fatal("changed file accepted")
	}
	if !reflect.DeepEqual(before, tree(t, home)) {
		t.Fatal("changed file overwritten")
	}
}

func TestInstallPreservesFirstBackup(t *testing.T) {
	isolatedHome(t)
	p, _ := Get("codex")
	ops, dir := operations(t, p, "http://h/mcp?namespace=first", "")
	original := []byte("# original config\n[other]\nvalue = 1\n")
	if err := os.MkdirAll(filepath.Dir(ops[0].File), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ops[0].File, original, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ns := range []string{"first", "second", "third"} {
		ops, _ = operations(t, p, "http://h/mcp?namespace="+ns, "")
		plan, err := Prepare(ops, skills.FS, dir, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := plan.Apply(t.Context(), false, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		backup, err := os.ReadFile(ops[0].File + ".smoot-bak")
		if err != nil || !bytes.Equal(backup, original) {
			t.Fatalf("%s lost original backup: %s %v", ns, backup, err)
		}
	}
}

func TestInstallCombinesOperationsAndPreservesModes(t *testing.T) {
	home := isolatedHome(t)
	path := filepath.Join(home, "settings.json")
	original := []byte("{\n// original\n\"other\":true\n}\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	ops := []Operation{
		{File: path, Op: "json-set", Path: []string{"hook"}, Value: `{"command":"test"}`},
		{File: path, Op: "json-ensure-strings", Path: []string{"permissions", "allow"}, Value: `["one","two"]`},
	}
	plan, err := Prepare(ops, fstest.MapFS{"test/SKILL.md": {Data: []byte("skill")}}, filepath.Join(home, "skills"), false)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := plan.Apply(t.Context(), false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "wrote "+path+"\n") != 1 || strings.Count(out.String(), "backup "+path+".smoot-bak") != 1 {
		t.Fatalf("multiple writes: %s", &out)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(got, []byte(`"hook"`)) || !bytes.Contains(got, []byte(`"allow"`)) {
		t.Fatalf("lost operation: %s %v", got, err)
	}
	backup, err := os.ReadFile(path + ".smoot-bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("backup not pre-install bytes: %s %v", backup, err)
	}
	for name, mode := range map[string]fs.FileMode{path: 0o640, path + ".smoot-bak": 0o600, filepath.Join(home, "skills", "test", "SKILL.md"): 0o644} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode: %v %v", name, info, err)
		}
	}
}

func TestInstallSymlinkRefusal(t *testing.T) {
	home := isolatedHome(t)
	target := filepath.Join(home, "original.json")
	link := filepath.Join(home, "settings.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	op := Operation{File: link, Op: "json-set", Path: []string{"smoot"}, Value: `{}`}
	if _, err := Prepare([]Operation{op}, fstest.MapFS{}, filepath.Join(home, "skills"), true); err == nil || !strings.Contains(err.Error(), link+" -> "+target) {
		t.Fatalf("symlink error: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "{}" {
		t.Fatalf("changed symlink target: %s %v", got, err)
	}
}

func TestCommandUsesInjectedHome(t *testing.T) {
	clientFixture(t)
	processHome := isolatedHome(t)
	injectedHome, override := t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": injectedHome, "CLAUDE_CONFIG_DIR": override}
	p, _ := Get("claude")
	ops, err := p.Operations(Setup{MCPURL: "http://h/mcp?namespace=default", MaxWait: time.Minute}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(ops, fstest.MapFS{}, filepath.Join(injectedHome, "skills"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(t.Context(), false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(override, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if len(tree(t, processHome)) != 0 {
		t.Fatal("command wrote into the process home")
	}
}

func TestLateApprovalConflictPreventsAllWrites(t *testing.T) {
	home := isolatedHome(t)
	p, _ := Get("antigravity")
	ops, dir := operations(t, p, "http://h/mcp?namespace=default", "")
	last := ops[len(ops)-1]
	if err := os.MkdirAll(filepath.Dir(last.File), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(last.File, []byte(`{"permissions":{"allow":42}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, home)
	if _, err := Prepare(ops, skills.FS, dir, true); err == nil {
		t.Fatal("invalid approval array accepted")
	}
	if !reflect.DeepEqual(before, tree(t, home)) {
		t.Fatal("conflict changed files")
	}
}

func TestCommandCannotShareFile(t *testing.T) {
	clientFixture(t)
	home := isolatedHome(t)
	p, _ := Get("claude")
	ops, dir := operations(t, p, "http://h/mcp?namespace=default", "")
	direct := Operation{File: ops[0].File, Op: "json-set", Path: []string{"other"}, Value: `true`}
	for _, pair := range [][]Operation{{ops[0], direct}, {direct, ops[0]}} {
		if _, err := Prepare(pair, fstest.MapFS{}, dir, false); err == nil || !strings.Contains(err.Error(), "cannot share target") {
			t.Fatalf("shared command target: %v", err)
		}
	}
	if len(tree(t, home)) != 0 {
		t.Fatal("preflight wrote files")
	}
}

func TestCodexAndPiShareSkills(t *testing.T) {
	home := isolatedHome(t)
	var firstDir string
	for _, name := range []string{"codex", "pi"} {
		p, _ := Get(name)
		ops, dir := operations(t, p, "http://h/mcp?namespace=default", "")
		if firstDir != "" && dir != firstDir {
			t.Fatalf("different skill roots: %s, %s", firstDir, dir)
		}
		firstDir = dir
		plan, err := Prepare(ops, skills.FS, dir, false)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := plan.Apply(t.Context(), false, &out); err != nil {
			t.Fatal(err)
		}
		if name == "pi" && strings.Contains(out.String(), "wrote "+dir) {
			t.Fatalf("Pi rewrote shared skills: %s", &out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".pi", "agent", "skills")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("duplicate Pi skills: %v", err)
	}
}

func TestCommandRemovesInheritedConfigOverride(t *testing.T) {
	clientFixture(t)
	processHome := isolatedHome(t)
	parentConfig := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", parentConfig)
	home := t.TempDir()
	p, _ := Get("claude")
	ops, err := p.Operations(Setup{MCPURL: "http://h/mcp?namespace=default", MaxWait: time.Minute}, func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(ops, fstest.MapFS{}, filepath.Join(home, "skills"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Apply(t.Context(), false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if len(tree(t, parentConfig)) != 0 || len(tree(t, processHome)) != 0 {
		t.Fatal("command used inherited config locations")
	}
}
