package profiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"github.com/tailscale/hujson"
)

// Edit computes a file change without writing. An unchanged result keeps the
// original bytes, including comments and formatting.
func (o Operation) Edit(old []byte, force bool) ([]byte, error) {
	switch o.Op {
	case "json-set", "json-append", "json-ensure-strings", "command":
		return o.editJSON(old, force)
	case "toml-table":
		return o.editTOML(old, force)
	default:
		return nil, fmt.Errorf("unknown op %q", o.Op)
	}
}

func jsonValue(raw []byte) (any, error) {
	v, err := hujson.Parse(raw)
	if err != nil {
		return nil, err
	}
	v.Standardize()
	var result any
	dec := json.NewDecoder(bytes.NewReader(v.Pack()))
	dec.UseNumber()
	err = dec.Decode(&result)
	return result, err
}

func pointer(path []string) string {
	var b strings.Builder
	for _, key := range path {
		b.WriteByte('/')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1"))
	}
	return b.String()
}

func patch(v *hujson.Value, path string, value any) error {
	action := "add"
	if v.Find(path) != nil {
		action = "replace"
	}
	raw, err := json.Marshal([]any{map[string]any{"op": action, "path": path, "value": value}})
	if err != nil {
		return err
	}
	return v.Patch(raw)
}

func (o Operation) editJSON(old []byte, force bool) ([]byte, error) {
	raw := old
	if len(raw) == 0 {
		raw = []byte("{}\n")
	}
	v, err := hujson.Parse(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := v.Value.(*hujson.Object); !ok {
		return nil, errors.New("config must be a json object")
	}
	want, err := jsonValue([]byte(o.Value))
	if err != nil {
		return nil, fmt.Errorf("profile value: %w", err)
	}
	for i := 1; i < len(o.Path); i++ {
		path := pointer(o.Path[:i])
		node := v.Find(path)
		if node == nil {
			if err := patch(&v, path, map[string]any{}); err != nil {
				return nil, err
			}
		} else if _, ok := node.Value.(*hujson.Object); !ok {
			return nil, fmt.Errorf("%s must be an object", path)
		}
	}
	path := pointer(o.Path)
	existing := v.Find(path)
	if o.Op == "json-ensure-strings" {
		return ensureStrings(&v, path, want, old)
	}
	if o.Op == "json-append" {
		if existing == nil {
			if err := patch(&v, path, []any{}); err != nil {
				return nil, err
			}
			existing = v.Find(path)
		}
		array, ok := existing.Value.(*hujson.Array)
		if !ok {
			return nil, fmt.Errorf("%s must be an array", path)
		}
		match := -1
		for i, elem := range array.Elements {
			value, err := jsonValue(elem.Pack())
			if err != nil {
				return nil, err
			}
			if hasCommand(value, o.Identity) {
				if match >= 0 {
					return nil, fmt.Errorf("multiple hooks match %q", o.Identity)
				}
				match = i
			}
		}
		if match < 0 {
			path += "/-"
			existing = nil
		} else {
			path += "/" + strconv.Itoa(match)
			existing = &array.Elements[match]
		}
	}
	if existing != nil {
		got, err := jsonValue(existing.Pack())
		if err != nil {
			return nil, err
		}
		if reflect.DeepEqual(got, want) {
			return old, nil
		}
		if !force {
			return nil, fmt.Errorf("%s differs; pass --force to replace", path)
		}
		if o.Op == "json-append" && !reflect.DeepEqual(hookShape(got, o.Identity), hookShape(want, o.Identity)) {
			return nil, fmt.Errorf("%s is a shared or customized hook group; put smoot's hook in its own group before replacing", path)
		}
	}
	if err := patch(&v, path, want); err != nil {
		return nil, err
	}
	// Format only a new document; patches retain surrounding source text.
	if len(old) == 0 {
		v.Format()
	}
	return v.Pack(), nil
}

// ensureStrings only adds absent strings. It never removes or reorders
// existing entries, has no conflict state, and is independent of --force.
func ensureStrings(v *hujson.Value, path string, want any, old []byte) ([]byte, error) {
	values, ok := want.([]any)
	if !ok {
		return nil, errors.New("profile approvals must be a string array")
	}
	var desired []string
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, errors.New("profile approvals must be a string array")
		}
		desired = append(desired, s)
	}
	seen := map[string]bool{}
	existing := v.Find(path)
	if existing != nil {
		array, ok := existing.Value.(*hujson.Array)
		if !ok {
			return nil, fmt.Errorf("%s must be a string array", path)
		}
		for _, item := range array.Elements {
			value, err := jsonValue(item.Pack())
			if err != nil {
				return nil, err
			}
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a string array", path)
			}
			seen[s] = true
		}
	}
	changed := false
	for _, s := range desired {
		if seen[s] {
			continue
		}
		if !changed && existing == nil {
			if err := patch(v, path, []any{}); err != nil {
				return nil, err
			}
		}
		if err := patch(v, path+"/-", s); err != nil {
			return nil, err
		}
		seen[s], changed = true, true
	}
	if !changed {
		return old, nil
	}
	if len(old) == 0 {
		v.Format()
	}
	return v.Pack(), nil
}

// hookShape reduces our own handler, an object whose command is ours and
// that holds no nested values, to the prefix, so its arguments and other
// fields (a status message, a timeout) are ours to replace. Extra hooks,
// matcher settings, or a command that runs more than ours mean the group
// belongs to the user.
func hookShape(value any, prefix string) any {
	switch v := value.(type) {
	case map[string]any:
		if command, ok := v["command"].(string); ok && ownCommand(command, prefix) && flat(v) {
			return prefix
		}
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = hookShape(child, prefix)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = hookShape(child, prefix)
		}
		return out
	default:
		return value
	}
}

func flat(m map[string]any) bool {
	for _, child := range m {
		switch child.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

// hasCommand reports whether value holds a command that starts with
// prefix as a whole word, so "prefix; other" counts and "prefixed" does
// not.
func hasCommand(value any, prefix string) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "command" {
				if command, ok := child.(string); ok && startsCommand(command, prefix) {
					return true
				}
			}
			if hasCommand(child, prefix) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasCommand(child, prefix) {
				return true
			}
		}
	}
	return false
}

func startsCommand(command, prefix string) bool {
	rest, ok := strings.CutPrefix(command, prefix)
	if !ok {
		return false
	}
	return rest == "" || !nameByte(rest[0])
}

func nameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

// ownCommand reports whether command is prefix followed only by words as
// shellQuote writes them, so nothing else runs with it.
func ownCommand(command, prefix string) bool {
	rest, ok := strings.CutPrefix(command, prefix)
	if !ok {
		return false
	}
	for rest != "" {
		if rest[0] != ' ' {
			return false
		}
		rest = strings.TrimLeft(rest, " ")
		for rest != "" && rest[0] != ' ' {
			switch {
			case rest[0] == '\'':
				end := strings.IndexByte(rest[1:], '\'')
				if end < 0 {
					return false
				}
				rest = rest[end+2:]
			case strings.HasPrefix(rest, `\'`):
				rest = rest[2:]
			case strings.IndexByte(shellSafe, rest[0]) >= 0:
				rest = rest[1:]
			default:
				return false
			}
		}
	}
	return true
}

func tableAt(root map[string]any, path []string) (any, bool, error) {
	var current any = root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false, errors.New("toml parent is not a table")
		}
		current, ok = object[key]
		if !ok {
			return nil, false, nil
		}
	}
	return current, true, nil
}

func (o Operation) editTOML(old []byte, force bool) ([]byte, error) {
	var got, want map[string]any
	if err := toml.Unmarshal(old, &got); err != nil {
		return nil, err
	}
	if err := toml.Unmarshal([]byte(o.Value), &want); err != nil {
		return nil, err
	}
	desired, ok, err := tableAt(want, o.Path)
	if err != nil || !ok {
		return nil, errors.New("profile value does not declare target table")
	}
	current, exists, err := tableAt(got, o.Path)
	if err != nil {
		return nil, err
	}
	if exists && reflect.DeepEqual(current, desired) {
		return old, nil
	}
	if exists && !force {
		return nil, fmt.Errorf("%s differs; pass --force to replace", strings.Join(o.Path, "."))
	}
	base := bytes.Clone(old)
	if exists {
		base, err = removeTOMLTable(old, o.Path)
		if err != nil {
			return nil, err
		}
	}
	if len(base) > 0 && base[len(base)-1] != '\n' {
		base = append(base, '\n')
	}
	if len(base) > 0 && !bytes.HasSuffix(base, []byte("\n\n")) {
		base = append(base, '\n')
	}
	base = append(base, []byte(o.Value)...)
	// Reparse the result to catch collisions with dotted keys or inline tables.
	var check map[string]any
	if err := toml.Unmarshal(base, &check); err != nil {
		return nil, fmt.Errorf("cannot safely append table: %w", err)
	}
	return base, nil
}

// removeTOMLTable uses parsed headers so multiline strings and quoted keys
// cannot masquerade as table boundaries. Inline/dotted layouts are refused.
func removeTOMLTable(raw []byte, path []string) ([]byte, error) {
	var out []byte
	end := 0
	target, found := false, false
	var p unstable.Parser
	p.Reset(raw)
	for p.NextExpression() {
		n := p.Expression()
		start, stop := 0, 0
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			it := n.Key()
			var keys []string
			for it.Next() {
				if len(keys) == 0 {
					start = int(it.Node().Raw.Offset)
				}
				keys = append(keys, string(it.Node().Data))
			}
			target = len(keys) >= len(path) && slices.Equal(keys[:len(path)], path)
			if !target {
				continue
			}
			found = true
			stop = start
		case unstable.KeyValue:
			if !target {
				continue
			}
			start = int(n.Raw.Offset)
			stop = int(n.Raw.Offset + n.Raw.Length)
		default:
			continue
		}
		start = bytes.LastIndexByte(raw[:start], '\n') + 1
		if i := bytes.IndexByte(raw[stop:], '\n'); i >= 0 {
			stop += i + 1
		} else {
			stop = len(raw)
		}
		out = append(out, raw[end:start]...)
		end = stop
	}
	if err := p.Error(); err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("cannot replace inline or dotted toml table; use an explicit table header")
	}
	return append(out, raw[end:]...), nil
}
