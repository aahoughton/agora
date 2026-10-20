package mcpserver_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aahoughton/smoot/internal/core"
	"github.com/aahoughton/smoot/internal/mcpserver"
	"github.com/aahoughton/smoot/internal/store"
)

// logLines decodes JSON log lines with the given msg.
func logLines(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func TestToolCallsAreLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	s := connect(t, newService(t), mcpserver.Options{Logger: logger})

	s.ok("start", map[string]any{"conversation": "c", "agent": "alice"})
	s.ok("say", map[string]any{"conversation": "c", "agent": "alice", "body": "a private thought"})
	s.ok("wait", map[string]any{"conversation": "c", "agent": "alice", "timeout_seconds": 1})
	s.call("say", map[string]any{"conversation": "c", "agent": "zed", "body": "hi"})
	s.call("read", map[string]any{"conversation": "c", "agent": "alice", "limit": "lots"})
	// A body of the wrong type is rejected by the schema, whose error quotes
	// the value; it must not reach the log either.
	s.call("say", map[string]any{"conversation": "c", "agent": "alice", "body": []string{"a private thought"}})

	if strings.Contains(buf.String(), "a private thought") {
		t.Fatalf("a message body was logged: %s", buf.String())
	}
	calls := logLines(t, &buf, "mcp call")
	if len(calls) != 6 {
		t.Fatalf("got %d call lines: %s", len(calls), buf.String())
	}
	want := []map[string]any{
		{"tool": "start", "conv": "c", "agent": "alice", "outcome": "ok", "ns": "default", "next": "wait", "expected": false},
		{"tool": "say", "agent": "alice", "outcome": "ok", "seq": float64(3), "body_bytes": float64(17), "next": "wait"},
		{"tool": "wait", "outcome": "ok", "timed_out": true, "new": float64(0), "timeout": float64(1e9)},
		{"tool": "say", "agent": "zed", "outcome": "not_participant", "level": "INFO"},
		{"tool": "read", "outcome": "rejected", "level": "INFO", "param": "limit"},
		{"tool": "say", "outcome": "rejected", "param": "body"},
	}
	for i, w := range want {
		for k, v := range w {
			if calls[i][k] != v {
				t.Errorf("call %d %s = %v, want %v (line %v)", i, k, calls[i][k], v, calls[i])
			}
		}
		for _, k := range []string{"dur", "out_bytes", "session"} {
			if _, ok := calls[i][k]; !ok {
				t.Errorf("call %d has no %s: %v", i, k, calls[i])
			}
		}
	}
}

func TestInternalErrorsAreLoggedWithCause(t *testing.T) {
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "smoot.db"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s := connect(t, core.New(db, core.Options{}), mcpserver.Options{Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	out, isErr := s.call("list", map[string]any{})
	if !isErr || out != "internal error" {
		t.Fatalf("list = %q, error %v", out, isErr)
	}
	calls := logLines(t, &buf, "mcp call")
	if len(calls) != 1 || calls[0]["level"] != "ERROR" || calls[0]["outcome"] != "internal" ||
		!strings.Contains(calls[0]["err"].(string), "closed") {
		t.Fatalf("calls = %v", calls)
	}
}
