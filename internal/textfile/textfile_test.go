package textfile

import (
	"bytes"
	"strings"
	"testing"
)

func TestTemplates(t *testing.T) {
	raw := "Preamble for editors.\n\n## greet\nfields: Name\nhello {{.Name}}\n\n## bye\nsee you\n"
	got, err := Templates(raw, []string{"greet", "bye"})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := got["greet"].Execute(&b, map[string]string{"Name": "ann"}); err != nil || b.String() != "hello ann" {
		t.Fatalf("greet = %q, %v", b.String(), err)
	}

	for _, tt := range []struct{ raw, want string }{
		{"## greet\nhi\n", "missing bye"},
		{"## greet\nhi\n## bye\nx\n## other\ny\n", `unknown key "other"`},
		{"## greet\nfields: Name\n## bye\nx\n", "greet is empty"},
		{"## greet\nhi\n## greet\nx\n", "duplicate section"},
		{"## Greet\nhi\n", "bad section key"},
		{"## greet\n{{.Oops\n## bye\nx\n", "greet"},
	} {
		if _, err := Templates(tt.raw, []string{"greet", "bye"}); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Templates(%q) err = %v, want %q", tt.raw, err, tt.want)
		}
	}
}
