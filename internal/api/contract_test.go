package api_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/api"
	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
)

var update = flag.Bool("update", false, "rewrite testdata/contract.golden")

// TestResponsesMatchTheContract records the JSON of every endpoint in a
// scripted run (fixed clock, secrets masked) and compares it with
// testdata/contract.golden. The response shapes come from core structs, so
// a renamed or removed field changes the public API; this test makes that
// a visible diff. After an intended change, run go test ./internal/api
// -update and review the golden alongside openapi.yaml.
func TestResponsesMatchTheContract(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	svc := core.New(db, core.Options{Policy: core.RolePolicy(), Now: func() time.Time { return clock }})
	root, err := svc.Bootstrap(t.Context(), "root")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.Register(mux, svc, auth.Bearer{Service: svc}, api.Options{Logger: slog.New(slog.DiscardHandler), AuthMode: "token", Version: "test"})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	token := root.Secret
	call := func(method, path string, body any) map[string]any {
		t.Helper()
		var rd io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			rd = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString("\n## " + method + " " + path + " -> " + http.StatusText(resp.StatusCode) + "\n")
		var v map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("%s %s: %s", method, path, raw)
			}
			pretty, _ := json.MarshalIndent(v, "", "  ")
			out.Write(mask(pretty))
			out.WriteString("\n")
		}
		return v
	}

	const c = "/v1/namespaces/default/conversations/design"
	call("GET", "/v1/whoami", nil)
	call("POST", "/v1/namespaces", map[string]any{"name": "team"})
	call("GET", "/v1/namespaces", nil)
	call("POST", "/v1/accounts", map[string]any{"name": "pat"})
	call("GET", "/v1/accounts", nil)
	call("PATCH", "/v1/accounts/pat", map[string]any{"disabled": false})
	issued := call("POST", "/v1/accounts/pat/tokens", map[string]any{"label": "laptop", "ttl_seconds": 3600})
	call("GET", "/v1/accounts/pat/tokens", nil)
	call("PUT", "/v1/namespaces/default/members/pat", map[string]any{"role": "member"})
	call("GET", "/v1/namespaces/default/members", nil)
	call("POST", "/v1/namespaces/default/conversations", map[string]any{"name": "design", "topic": "pick a cache", "end": "agreement", "agent": "alice"})
	call("GET", "/v1/namespaces/default/conversations?status=all", nil)
	call("GET", c, nil)
	token = issued["secret"].(string)
	call("POST", c+"/join", map[string]any{"agent": "bob"})
	call("POST", c+"/presence", map[string]any{"agent": "bob", "presence": "working", "note": "reading"})
	token = root.Secret
	call("POST", c+"/messages", map[string]any{"agent": "alice", "body": "LRU or LFU?", "idempotency_key": "q1"})
	call("POST", c+"/observe", map[string]any{"name": "ignored", "body": "mind the budget"})
	token = issued["secret"].(string)
	call("POST", c+"/wait", map[string]any{"agent": "bob", "timeout_seconds": 1, "inline_bytes": 1000})
	call("POST", c+"/messages", map[string]any{"agent": "bob", "body": "LRU", "reply_to": 3})
	call("POST", c+"/read", map[string]any{"agent": "bob"})
	call("GET", c+"/messages?tail=2", nil)
	call("GET", c+"/follow?after=4&timeout=0", nil)
	call("GET", "/v1/namespaces/default/status?agent=bob", nil)
	call("POST", c+"/conclude", map[string]any{"agent": "bob", "body": "AGREED: LRU"})
	call("POST", c+"/leave", map[string]any{"agent": "bob"})
	token = root.Secret
	call("POST", c+"/conclude", map[string]any{"agent": "alice", "body": "AGREED: LRU"})
	call("POST", "/v1/namespaces/default/conversations", map[string]any{"name": "led", "agent": "lead", "end": "coordinator"})
	call("POST", "/v1/namespaces/default/conversations/led/close", map[string]any{"agent": "lead", "body": "done"})
	call("POST", "/v1/accounts/pat/tokens/revoke", map[string]any{})
	call("DELETE", "/v1/namespaces/default/members/pat", nil)
	call("POST", c+"/messages", map[string]any{"agent": "alice", "body": "late"})

	path := filepath.Join("testdata", "contract.golden")
	if *update {
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/api -update to create it)", err)
	}
	if got := out.String(); got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := range min(len(gl), len(wl)) {
			if gl[i] != wl[i] {
				t.Fatalf("API responses changed at line %d:\n got: %s\nwant: %s\nreview, then run go test ./internal/api -update", i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("API responses changed in length (%d vs %d lines); run go test ./internal/api -update after review", len(gl), len(wl))
	}
}

var (
	secretRE = regexp.MustCompile(`"agoras?_[a-z0-9]+"`)
	timeRE   = regexp.MustCompile(`"\d{4}-\d\d-\d\dT[0-9:.]+Z"`)
)

// mask hides token secrets, which are random, and times not on the test's
// clock (the default namespace is created by the migration, at real time).
func mask(b []byte) []byte {
	b = secretRE.ReplaceAll(b, []byte(`"agora_<secret>"`))
	return timeRE.ReplaceAllFunc(b, func(ts []byte) []byte {
		if bytes.HasPrefix(ts, []byte(`"2030-`)) {
			return ts
		}
		return []byte(`"<real time>"`)
	})
}
