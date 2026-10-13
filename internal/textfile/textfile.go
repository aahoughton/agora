// Package textfile parses the Markdown files that hold user- and
// agent-facing text: a preamble for editors, then one "## key" section per
// piece of text.
package textfile

import (
	"bufio"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/template"
)

// Section is one "## key" section of a file.
type Section struct {
	Key  string
	Body string
}

var keyRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Sections splits raw on "## key" headings. Text before the first heading
// is a preamble for editors and is ignored. Bodies are trimmed.
func Sections(raw string) ([]Section, error) {
	var out []Section
	seen := map[string]bool{}
	var cur *Section
	var body strings.Builder
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(body.String())
			out = append(out, *cur)
		}
		body.Reset()
	}
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		if key, ok := strings.CutPrefix(line, "## "); ok {
			flush()
			key = strings.TrimSpace(key)
			if !keyRE.MatchString(key) {
				return nil, fmt.Errorf("bad section key %q", key)
			}
			if seen[key] {
				return nil, fmt.Errorf("duplicate section %q", key)
			}
			seen[key] = true
			cur = &Section{Key: key}
			continue
		}
		if cur != nil {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	flush()
	return out, sc.Err()
}

// Templates parses raw into one text/template per section and requires
// exactly the given keys. A section may start with a "fields: ..." line
// that documents the template's data for editors; it is dropped.
func Templates(raw string, keys []string) (map[string]*template.Template, error) {
	secs, err := Sections(raw)
	if err != nil {
		return nil, err
	}
	out := map[string]*template.Template{}
	for _, s := range secs {
		if !slices.Contains(keys, s.Key) {
			return nil, fmt.Errorf("unknown key %q", s.Key)
		}
		body := s.Body
		if first, rest, ok := strings.Cut(body, "\n"); ok && strings.HasPrefix(first, "fields:") {
			body = rest
		} else if strings.HasPrefix(body, "fields:") {
			body = ""
		}
		body = strings.TrimSpace(body)
		if body == "" {
			return nil, fmt.Errorf("%s is empty", s.Key)
		}
		t, err := template.New(s.Key).Option("missingkey=error").Parse(body)
		if err != nil {
			return nil, err
		}
		out[s.Key] = t
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("missing %s", k)
		}
	}
	return out, nil
}
