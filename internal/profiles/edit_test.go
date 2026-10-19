package profiles

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestJSONAppendIdentity(t *testing.T) {
	op := Operation{Op: "json-append", Path: []string{"hooks", "Stop"}, Identity: "agora hook stop", Value: `{"hooks":[{"type":"command","command":"agora hook stop --url new"}]}`}
	old := []byte(`{
 // preserve
 "hooks":{"Stop":[
 {"hooks":[{"type":"command","command":"agora hook stopping"}]},
 {"hooks":[{"type":"command","command":"agora hook stop --url old"}]},
 {"hooks":[{"type":"command","command":"echo other"}]}
 ]}
}`)
	if _, err := op.Edit(old, false); err == nil {
		t.Fatal("accepted differing match")
	}
	got, err := op.Edit(old, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"// preserve", "agora hook stopping", "echo other", "agora hook stop --url new"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("lost %q: %s", want, got)
		}
	}
	if bytes.Contains(got, []byte("--url old")) {
		t.Fatal("old hook remained")
	}
	twice, err := op.Edit(got, false)
	if err != nil || !bytes.Equal(got, twice) {
		t.Fatalf("not idempotent: %s %v", twice, err)
	}
	duplicate := bytes.Replace(got, []byte("agora hook stopping"), []byte("agora hook stop --url duplicate"), 1)
	if _, err := op.Edit(duplicate, true); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	for _, invalid := range []string{`{"hooks":null}`, `{"hooks":{"Stop":null}}`, `{"hooks":{"Stop":{}}}`, `{"hooks":[]}`, `null`, `[]`, `{oops}`} {
		if _, err := op.Edit([]byte(invalid), true); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}

func TestJSONSetPreservesUnrelatedBytes(t *testing.T) {
	op := Operation{Op: "json-set", Path: []string{"mcpServers", "agora"}, Value: `{"url":"new"}`}
	old := []byte("{\n // keep\n \"mcpServers\": {\"other\": {\"url\": \"old\"},},\n \"tail\": true, // keep too\n}\n")
	got, err := op.Edit(old, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"// keep", `"other": {"url": "old"}`, `"tail": true, // keep too`} {
		if !bytes.Contains(got, []byte(want)) {
			t.Fatalf("lost %q: %s", want, got)
		}
	}
}

func TestTOMLTableEdits(t *testing.T) {
	op := Operation{Op: "toml-table", Path: []string{"mcp_servers", "agora"}, Value: "[mcp_servers.agora]\nurl = \"new\"\n"}
	for name, old := range map[string]string{
		"explicit":  "# before\n[mcp_servers.agora]\nurl = \"old\"\n\n# preserve next\n[other]\nx = 1\n",
		"quoted":    "[mcp_servers.\"agora\"]\nurl = \"old\"\n# preserve next\n[other]\nx = 1\n",
		"multiline": "[mcp_servers.agora]\nurl = '''\n[misleading]\n'''\n# preserve next\n[other]\nx = 1\n",
		"subtable":  "[mcp_servers.agora]\nurl = \"old\"\n[mcp_servers.agora.headers]\nx = \"y\"\n# preserve next\n[other]\nx = 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := op.Edit([]byte(old), false); err == nil {
				t.Fatal("conflict accepted")
			}
			got, err := op.Edit([]byte(old), true)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(got, []byte("# preserve next\n[other]\nx = 1\n")) {
				t.Fatalf("lost unrelated bytes: %s", got)
			}
			next, err := op.Edit(got, false)
			if err != nil || !bytes.Equal(got, next) {
				t.Fatalf("not idempotent: %s %v", next, err)
			}
		})
	}
	for _, old := range []string{"mcp_servers.agora = {url = \"old\"}\n", "[mcp_servers]\nagora.url = \"old\"\n", "mcp_servers = 42\n"} {
		if _, err := op.Edit([]byte(old), true); err == nil {
			t.Fatalf("accepted unsupported layout %q", old)
		}
	}
	same := []byte("[mcp_servers.\"agora\"] # comment\nurl='new'\n")
	if got, err := op.Edit(same, false); err != nil || !bytes.Equal(got, same) {
		t.Fatalf("equivalent table changed: %s %v", got, err)
	}
	old := []byte("[other]\ntext = '''\n[mcp_servers.agora]\n'''\n")
	got, err := op.Edit(old, false)
	if err != nil || !strings.HasPrefix(string(got), string(old)) {
		t.Fatalf("string mistaken for table: %s %v", got, err)
	}
}

func TestJSONAppendRefusesSharedGroup(t *testing.T) {
	op := Operation{Op: "json-append", Path: []string{"hooks", "Stop"}, Identity: "agora hook stop", Value: `{"hooks":[{"type":"command","command":"agora hook stop --url new"}]}`}
	for _, value := range []string{
		`{"hooks":[{"type":"command","command":"say done"},{"type":"command","command":"agora hook stop --url old"}]}`,
		`{"hooks":[{"type":"prompt","prompt":"check work"},{"type":"command","command":"agora hook stop --url old"}]}`,
		`{"matcher":"custom","hooks":[{"type":"command","command":"agora hook stop --url old"}]}`,
		`{"hooks":[{"type":"command","command":"agora hook stop; say done"}]}`,
		`{"hooks":[{"type":"command","command":"agora hook stop --url old && say done"}]}`,
		`{"hooks":[{"type":"command","command":"agora hook stop --url 'a b' | tee log"}]}`,
		`{"hooks":[{"type":"command","command":"agora hook stop","hooks":[{"type":"command","command":"say done"}]}]}`,
	} {
		old := []byte(`{"hooks":{"Stop":[` + value + `]}}`)
		if _, err := op.Edit(old, true); err == nil {
			t.Fatalf("force accepted shared/customized group: %s", old)
		}
	}
}

func TestJSONAppendReplacesOwnHandler(t *testing.T) {
	op := Operation{Op: "json-append", Path: []string{"hooks", "Stop"}, Identity: "agora hook stop", Value: `{"hooks":[{"type":"command","command":"agora hook stop --url new","statusMessage":"agora"}]}`}
	for _, command := range []string{
		`agora hook stop`,
		`agora hook stop --url old --namespace default`,
		`agora hook stop --url 'http://h/?a=1&b=2' --namespace 'it'\''s'`,
	} {
		old := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":` + strconv.Quote(command) + `,"timeout":5}]}]}}`)
		got, err := op.Edit(old, true)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if !bytes.Contains(got, []byte(`"statusMessage":"agora"`)) || bytes.Contains(got, []byte(`"timeout"`)) || bytes.Count(got, []byte("agora hook stop")) != 1 {
			t.Fatalf("%s: got %s", command, got)
		}
	}
}

func TestEnsureStringsIsAdditive(t *testing.T) {
	op := Operation{Op: "json-ensure-strings", Path: []string{"permissions", "allow"}, Value: `["new", "existing", "new", "last"]`}
	old := []byte(`{
 // preserve
 "permissions": {"allow": ["unrelated", "existing", "unrelated",], "deny": ["secret"]}
}`)
	for _, force := range []bool{false, true} {
		got, err := op.Edit(old, force)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(got, []byte("// preserve")) || !bytes.Contains(got, []byte(`"deny": ["secret"]`)) {
			t.Fatalf("lost unrelated bytes: %s", got)
		}
		value, err := jsonValue(got)
		if err != nil {
			t.Fatal(err)
		}
		allow := value.(map[string]any)["permissions"].(map[string]any)["allow"]
		if !reflect.DeepEqual(allow, []any{"unrelated", "existing", "unrelated", "new", "last"}) {
			t.Fatalf("reordered permissions: %v", allow)
		}
		again, err := op.Edit(got, force)
		if err != nil || !bytes.Equal(again, got) {
			t.Fatalf("second install changed bytes: %s %v", again, err)
		}
	}
	for _, old := range []string{`{"permissions":{"allow":null}}`, `{"permissions":{"allow":[true]}}`, `{"permissions":{"allow":"all"}}`} {
		if _, err := op.Edit([]byte(old), true); err == nil {
			t.Fatalf("accepted invalid array: %s", old)
		}
	}
}
