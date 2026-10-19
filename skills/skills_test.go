package skills_test

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/aahoughton/smoot/internal/core"
	"github.com/aahoughton/smoot/internal/wording"
	"github.com/aahoughton/smoot/skills"
)

func skillFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(skills.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		raw, err := fs.ReadFile(skills.FS, path)
		out[path] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFrontmatter checks what both Claude Code and Codex need: a SKILL.md
// per skill whose name matches its directory, and a description.
func TestFrontmatter(t *testing.T) {
	dirs, err := fs.ReadDir(skills.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		raw, err := fs.ReadFile(skills.FS, d.Name()+"/SKILL.md")
		if err != nil {
			t.Fatalf("%s: %v", d.Name(), err)
		}
		head, _, ok := strings.Cut(strings.TrimPrefix(string(raw), "---\n"), "\n---\n")
		if !strings.HasPrefix(string(raw), "---\n") || !ok {
			t.Fatalf("%s: no frontmatter", d.Name())
		}
		fields := map[string]string{}
		for line := range strings.SplitSeq(head, "\n") {
			k, v, _ := strings.Cut(line, ": ")
			fields[k] = v
		}
		if fields["name"] != d.Name() {
			t.Errorf("%s: name %q", d.Name(), fields["name"])
		}
		if desc := fields["description"]; desc == "" || len(desc) > 1024 {
			t.Errorf("%s: description is empty or longer than 1024 bytes", d.Name())
		}
	}
}

// protocolTerms are backticked words a skill may use that are not tools,
// parameters or error codes: status-line values, end conditions, roles and
// example names. Adding a word here is a decision that it means what the
// server means by it.
var protocolTerms = []string{
	"expected", "next", "observer", "agreement", "coordinator",
	"codex-reviewer", "gemini-implementer",
}

// TestNamesExist fails when a skill mentions a tool, parameter or error code
// that the server does not have, such as one renamed or never built.
func TestNamesExist(t *testing.T) {
	w := wording.Default()
	known := map[string]bool{}
	for _, name := range w.Tools() {
		known[name] = true
		tool, _ := w.Tool(name)
		for p := range tool.Params {
			known[p] = true
		}
	}
	for _, c := range []core.Code{
		core.CodeConversationNotFound, core.CodeNamespaceNotFound, core.CodeNameActive,
		core.CodeConversationConcluded, core.CodeStaleContext, core.CodeTooManyWaiters, core.CodeNotParticipant,
	} {
		known[string(c)] = true
	}
	for _, term := range protocolTerms {
		known[term] = true
	}
	dirs, err := fs.ReadDir(skills.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		known[d.Name()] = true
	}
	word := regexp.MustCompile("`([a-z][a-z0-9_-]*)(?::[^`]*)?`")
	for path, text := range skillFiles(t) {
		for _, m := range word.FindAllStringSubmatch(text, -1) {
			if !known[m[1]] {
				t.Errorf("%s mentions `%s`, which is not a tool, parameter, error code or listed term", path, m[1])
			}
		}
	}
	// The core of the protocol must be named somewhere.
	all := strings.Join(slices.Collect(func(yield func(string) bool) {
		for _, text := range skillFiles(t) {
			if !yield(text) {
				return
			}
		}
	}), "\n")
	for _, tool := range []string{"join", "read", "say", "wait", "conclude", "start", "list", "status"} {
		if !strings.Contains(all, "`"+tool+"`") {
			t.Errorf("no skill mentions `%s`", tool)
		}
	}
}
