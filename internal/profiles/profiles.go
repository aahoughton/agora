// Package profiles describes the agent clients agora knows how to set up:
// how each one connects to the MCP endpoint, where it reads skills from,
// and how it registers a Stop hook. Each client is one Markdown file under
// text/, so adding a client means adding a file.
package profiles

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/aahoughton/agora/internal/textfile"
)

//go:embed text/*.md
var files embed.FS

// keys are the sections every profile file must have.
var keys = []string{"description", "skills_dir", "mcp_config", "stop_hook"}

// clientRequestLimit is the per-call limit both supported clients apply to
// an HTTP MCP tool call by default.
const clientRequestLimit = 60 * time.Second

// waitMargin is how much longer than the server's longest wait a client
// should allow a tool call to run.
const waitMargin = 10 * time.Second

// Profile is one client.
type Profile struct {
	Name        string
	Description string
	// SkillsDir is where the client reads user-level skills, with a
	// leading ~ for the home directory.
	SkillsDir string
	mcpConfig *template.Template
	stopHook  *template.Template
}

var all = mustLoad(files)

func mustLoad(fsys fs.FS) map[string]*Profile {
	ps, err := load(fsys)
	if err != nil {
		panic(err)
	}
	return ps
}

func load(fsys fs.FS) (map[string]*Profile, error) {
	paths, err := fs.Glob(fsys, "text/*.md")
	if err != nil {
		return nil, err
	}
	out := map[string]*Profile{}
	for _, path := range paths {
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(strings.TrimPrefix(path, "text/"), ".md")
		ts, err := textfile.Templates(string(raw), keys)
		if err != nil {
			return nil, fmt.Errorf("profiles: %s: %w", path, err)
		}
		p := &Profile{Name: name, mcpConfig: ts["mcp_config"], stopHook: ts["stop_hook"]}
		if p.Description, err = run(ts["description"], nil); err != nil {
			return nil, fmt.Errorf("profiles: %s: %w", path, err)
		}
		p.Description = strings.TrimSpace(p.Description)
		if p.SkillsDir, err = run(ts["skills_dir"], nil); err != nil {
			return nil, fmt.Errorf("profiles: %s: %w", path, err)
		}
		p.SkillsDir = strings.TrimSpace(p.SkillsDir)
		out[name] = p
	}
	return out, nil
}

// Names lists the profiles, sorted.
func Names() []string {
	var names []string
	for n := range all {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Get returns the named profile.
func Get(name string) (*Profile, error) {
	p, ok := all[name]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q: want one of %s", name, strings.Join(Names(), ", "))
	}
	return p, nil
}

// MCPConfig renders the client's setup for the MCP endpoint mcpURL, on a
// server whose longest MCP wait is maxWait. If tokenEnv is set, the client
// is set up to send the token in that environment variable; the setup
// never contains a token itself.
func (p *Profile) MCPConfig(mcpURL string, maxWait time.Duration, tokenEnv string) (string, error) {
	data := map[string]any{
		"MCPURL": mcpURL, "MCPURLShell": shellQuote(mcpURL), "MCPURLString": quoteString(mcpURL),
		"TimeoutMS": 0, "TimeoutSeconds": 0, "TokenEnv": tokenEnv, "TokenEnvString": quoteString(tokenEnv),
	}
	// Raise the client's limit only when the server's waits would hit it.
	if need := maxWait + waitMargin; need > clientRequestLimit {
		data["TimeoutMS"] = need.Milliseconds()
		data["TimeoutSeconds"] = int((need + time.Second - 1) / time.Second)
	}
	return run(p.mcpConfig, data)
}

// StopHook renders the client's Stop hook entry running command.
func (p *Profile) StopHook(command string) (string, error) {
	return run(p.stopHook, map[string]any{"Command": command, "CommandString": quoteString(command)})
}

// shellQuote quotes s as one POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// quoteString quotes s as a JSON string, which is also a valid TOML basic
// string.
func quoteString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

func run(t *template.Template, data any) (string, error) {
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}
