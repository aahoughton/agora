// Package profiles describes client setup as data and applies a closed set
// of install operations. Each client is one Markdown file under text/.
package profiles

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/aahoughton/agora/internal/textfile"
)

//go:embed text/*.md
var files embed.FS

// Profile is one client's config root, skills directory and install recipes.
type Profile struct {
	Name, Description, SkillsDir, ConfigDir, HomeEnv, HookContract string
	ApprovalsNote                                                  string
	installs                                                       map[string]recipe
	order                                                          []string
}

type recipe struct {
	Operation
	value *template.Template
}

// Operation is a rendered install operation. Command operations read File at
// Path to check equality, then invoke Run (and Replace for a forced update).
type Operation struct {
	File            string            `json:"file"`
	FileWhenHomeEnv string            `json:"file_when_home_env,omitempty"`
	Op              string            `json:"op"`
	Path            []string          `json:"path"`
	Identity        string            `json:"identity,omitempty"`
	Note            string            `json:"note,omitempty"`
	Run             []string          `json:"run,omitempty"`
	Replace         []string          `json:"replace,omitempty"`
	Value           string            `json:"-"`
	CommandEnv      map[string]string `json:"-"`
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
		p, err := parseProfile(string(raw))
		if err != nil {
			return nil, fmt.Errorf("profiles: %s: %w", path, err)
		}
		p.Name = strings.TrimSuffix(strings.TrimPrefix(path, "text/"), ".md")
		out[p.Name] = p
	}
	return out, nil
}

func parseProfile(raw string) (*Profile, error) {
	secs, err := textfile.Sections(raw)
	if err != nil {
		return nil, err
	}
	p := &Profile{installs: map[string]recipe{}}
	for _, s := range secs {
		switch s.Key {
		case "description":
			p.Description = s.Body
		case "skills_dir":
			p.SkillsDir = s.Body
		case "config_dir":
			p.ConfigDir = s.Body
		case "home_env":
			p.HomeEnv = s.Body
		case "hook_contract":
			p.HookContract = s.Body
		case "approvals_note":
			p.ApprovalsNote = s.Body
		case "install_mcp", "install_stop_hook", "install_approvals":
			meta, value, ok := strings.Cut(s.Body, "\n")
			if !ok {
				return nil, fmt.Errorf("%s needs metadata and a value", s.Key)
			}
			var r recipe
			dec := json.NewDecoder(strings.NewReader(meta))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&r.Operation); err != nil {
				return nil, err
			}
			if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
				return nil, errors.New("trailing metadata")
			}
			if !slices.Contains([]string{"json-set", "json-append", "json-ensure-strings", "toml-table", "command"}, r.Op) {
				return nil, fmt.Errorf("unknown op %q", r.Op)
			}
			if r.File == "" || len(r.Path) == 0 {
				return nil, errors.New("install needs file and path")
			}
			if r.Op == "json-append" && r.Identity == "" {
				return nil, errors.New("json-append needs identity")
			}
			if r.Op == "command" && (len(r.Run) == 0 || len(r.Replace) == 0) {
				return nil, errors.New("command needs run and replace argv")
			}
			if r.Op != "command" && (len(r.Run) != 0 || len(r.Replace) != 0) {
				return nil, errors.New("argv requires command op")
			}
			r.value, err = parseTemplate(s.Key, value)
			if err != nil {
				return nil, err
			}
			for _, text := range append(append([]string{r.Note}, r.Run...), r.Replace...) {
				if _, err := parseTemplate(s.Key, text); err != nil {
					return nil, err
				}
			}
			p.installs[s.Key] = r
			p.order = append(p.order, s.Key)
		default:
			return nil, fmt.Errorf("unknown key %q", s.Key)
		}
	}
	for _, required := range []struct{ key, value string }{{"description", p.Description}, {"skills_dir", p.SkillsDir}, {"config_dir", p.ConfigDir}} {
		if required.value == "" {
			return nil, fmt.Errorf("missing %s", required.key)
		}
	}
	if !strings.HasPrefix(p.ConfigDir, "~/") {
		return nil, errors.New("config_dir must start with ~/")
	}
	if _, ok := p.installs["install_mcp"]; !ok {
		return nil, errors.New("missing install_mcp")
	}
	if p.HookContract != "" && !slices.Contains([]string{"claude", "antigravity"}, p.HookContract) {
		return nil, fmt.Errorf("unknown hook contract %q", p.HookContract)
	}
	_, hook := p.installs["install_stop_hook"]
	if hook != (p.HookContract != "") {
		return nil, errors.New("stop hook and hook_contract must be declared together")
	}
	for _, r := range p.installs {
		if r.FileWhenHomeEnv != "" && p.HomeEnv == "" {
			return nil, errors.New("file_when_home_env requires home_env")
		}
	}
	return p, nil
}

// ResolveOperation expands a recipe's file, including an explicit override.
func (p *Profile) ResolveOperation(op Operation, getenv func(string) string) (Operation, error) {
	path := op.File
	if p.HomeEnv != "" && getenv(p.HomeEnv) != "" && op.FileWhenHomeEnv != "" {
		path = op.FileWhenHomeEnv
	}
	var err error
	op.File, err = p.Resolve(path, getenv)
	if op.Op == "command" {
		op.CommandEnv = map[string]string{"HOME": getenv("HOME")}
		if p.HomeEnv != "" {
			op.CommandEnv[p.HomeEnv] = getenv(p.HomeEnv)
		}
	}
	return op, err
}

// Setup contains template inputs and selected optional sections. TokenEnv is
// an environment variable name; no token value is accepted.
type Setup struct {
	MCPURL       string
	MaxWait      time.Duration
	TokenEnv     string
	HookCommand  string
	ApproveTools bool
	ToolNames    []string
}

// Operations renders and resolves the selected sections in profile order.
func (p *Profile) Operations(setup Setup, getenv func(string) string) ([]Operation, error) {
	mcp, err := p.MCPOperation(setup.MCPURL, setup.MaxWait, setup.TokenEnv)
	if err != nil {
		return nil, err
	}
	selected := map[string]Operation{"install_mcp": mcp}
	if setup.HookCommand != "" {
		op, err := p.HookOperation(setup.HookCommand)
		if err != nil {
			return nil, err
		}
		selected["install_stop_hook"] = op
	}
	if setup.ApproveTools {
		if _, ok := p.installs["install_approvals"]; !ok {
			return nil, fmt.Errorf("%s has no separate tool approval setup: %s", p.Name, p.ApprovalsNote)
		}
		op, err := p.render("install_approvals", map[string]any{"ToolNames": setup.ToolNames})
		if err != nil {
			return nil, err
		}
		selected["install_approvals"] = op
	}
	var ops []Operation
	for _, key := range p.order {
		op, ok := selected[key]
		if !ok {
			continue
		}
		op, err = p.ResolveOperation(op, getenv)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
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

// Resolve expands ~/ under HOME and @/ under the profile's config root.
func (p *Profile) Resolve(path string, getenv func(string) string) (string, error) {
	base, rest, ok := strings.Cut(path, "/")
	if !ok || (base != "~" && base != "@") {
		return "", fmt.Errorf("profile path must start with ~/ or @/: %q", path)
	}
	root := getenv("HOME")
	if base == "@" {
		root = getenv(p.HomeEnv)
		if root == "" {
			var err error
			root, err = p.Resolve(p.ConfigDir, getenv)
			if err != nil {
				return "", err
			}
		}
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("profile home must be absolute, got %q", root)
	}
	if slices.Contains(strings.Split(rest, "/"), "..") {
		return "", errors.New("profile path escapes home")
	}
	return filepath.Join(root, filepath.FromSlash(rest)), nil
}

// MCPOperation renders setup without ever accepting a token value.
func (p *Profile) MCPOperation(mcpURL string, maxWait time.Duration, tokenEnv string) (Operation, error) {
	u, err := url.Parse(mcpURL)
	if err != nil {
		return Operation{}, err
	}
	ns := u.Query().Get("namespace")
	u.RawQuery = ""
	u.Path = strings.TrimSuffix(u.Path, "/mcp")
	data := map[string]any{"MCPURLString": quoteString(mcpURL), "BaseURLString": quoteString(u.String()), "NamespaceString": quoteString(ns), "TimeoutMS": int64(0), "TimeoutSeconds": int64(0), "TokenEnv": tokenEnv, "TokenEnvString": quoteString(tokenEnv)}
	if need := maxWait + 10*time.Second; need > 60*time.Second {
		data["TimeoutMS"] = need.Milliseconds()
		data["TimeoutSeconds"] = int64((need + time.Second - 1) / time.Second)
	}
	return p.render("install_mcp", data)
}

// HookOperation renders the hook entry for a shell command.
func (p *Profile) HookOperation(command string) (Operation, error) {
	if p.HookContract == "antigravity" {
		command += " --client antigravity"
	}
	return p.render("install_stop_hook", map[string]any{"CommandString": quoteString(command)})
}

func (p *Profile) render(key string, data map[string]any) (Operation, error) {
	r, ok := p.installs[key]
	if !ok {
		return Operation{}, fmt.Errorf("%s has no supported stop hook", p.Name)
	}
	op := r.Operation
	var err error
	if op.Value, err = run(r.value, data); err != nil {
		return Operation{}, err
	}
	data["ValueJSON"] = strings.TrimSpace(op.Value)
	render := func(s string) (string, error) {
		t, err := parseTemplate(key, s)
		if err != nil {
			return "", err
		}
		out, err := run(t, data)
		return strings.TrimSpace(out), err
	}
	if op.Note, err = render(op.Note); err != nil {
		return Operation{}, err
	}
	op.Run, op.Replace = slices.Clone(r.Run), slices.Clone(r.Replace)
	for _, argv := range [][]string{op.Run, op.Replace} {
		for i, s := range argv {
			if argv[i], err = render(s); err != nil {
				return Operation{}, err
			}
		}
	}
	return op, nil
}

// Instructions prints from the same rendered operation the installer uses.
func (o Operation) Instructions() string {
	var b strings.Builder
	if o.Op == "command" {
		for i, arg := range o.Run {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(shellQuote(arg))
		}
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "# add to %s at %s:\n%s", o.File, strings.Join(o.Path, "."), o.Value)
	}
	if o.Note != "" {
		fmt.Fprintf(&b, "# %s\n", strings.ReplaceAll(o.Note, "\n", "\n# "))
	}
	return b.String()
}

func parseTemplate(name, text string) (*template.Template, error) {
	return template.New(name).Funcs(template.FuncMap{"json": quoteString}).Option("missingkey=error").Parse(text)
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./:@%+=,-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func quoteString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func run(t *template.Template, data any) (string, error) {
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}
