package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aahoughton/agora/internal/core"
)

func TestAntigravityHook(t *testing.T) {
	url, svc := setup(t)
	for _, tt := range []struct {
		name, input, agent string
		hold               bool
	}{
		{"expected", `{"conversationId":"s","terminationReason":"NO_TOOL_CALL","fullyIdle":true}`, "bob", true},
		{"idle is not a stop reason", `{"conversationId":"s","terminationReason":"NO_TOOL_CALL","fullyIdle":false}`, "bob", true},
		{"cancelled", `{"conversationId":"s","terminationReason":"USER_CANCELLED","fullyIdle":true}`, "bob", false},
		{"error", `{"conversationId":"s","terminationReason":"ERROR"}`, "bob", false},
		{"missing reason", `{"conversationId":"s"}`, "bob", false},
		{"missing session", `{"terminationReason":"NO_TOOL_CALL"}`, "bob", false},
		{"not expected", `{"conversationId":"s","terminationReason":"NO_TOOL_CALL"}`, "carol", false},
		{"no agent", `{"conversationId":"s","terminationReason":"NO_TOOL_CALL"}`, "", false},
		{"malformed", `not json`, "bob", false},
		{"wrong contract fields", `{"session_id":"s","stop_hook_active":false}`, "bob", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"AGORA_AGENT": tt.agent, "XDG_CACHE_HOME": t.TempDir()}
			r := runCLIInput(t.Context(), url, tt.input, env, "hook", "stop", "--client", "antigravity")
			if r.code != exitOK {
				t.Fatalf("hook: %+v", r)
			}
			if !tt.hold {
				if r.stdout != "" {
					t.Fatalf("held stop: %+v", r)
				}
				return
			}
			var response struct{ Decision, Reason string }
			if err := json.Unmarshal([]byte(r.stdout), &response); err != nil || response.Decision != "continue" || !strings.Contains(response.Reason, "expected") {
				t.Fatalf("response: %s %v", r.stdout, err)
			}
		})
	}
	t.Run("patience and progress", func(t *testing.T) {
		env := map[string]string{"AGORA_AGENT": "bob", "XDG_CACHE_HOME": t.TempDir()}
		stop := func(session string) bool {
			t.Helper()
			r := runCLIInput(t.Context(), url, `{"conversationId":"`+session+`","terminationReason":"NO_TOOL_CALL"}`, env, "hook", "stop", "--client", "antigravity", "--patience", "2")
			if r.code != exitOK {
				t.Fatal(r)
			}
			return strings.Contains(r.stdout, `"decision":"continue"`)
		}
		for _, want := range []bool{true, true, false, false} {
			if stop("s1") != want {
				t.Fatal("patience did not release unchanged session")
			}
		}
		if !stop("s2") {
			t.Fatal("sessions shared patience")
		}
		if _, err := svc.Say(t.Context(), core.Anonymous(), "default", "c", core.SayParams{Agent: "alice", Body: "new progress"}); err != nil {
			t.Fatal(err)
		}
		if !stop("s1") {
			t.Fatal("progress did not reset patience")
		}
	})
	t.Run("state unavailable", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(bad, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, cache := range []string{"", bad} {
			env := map[string]string{"AGORA_AGENT": "bob", "XDG_CACHE_HOME": cache}
			r := runCLIInput(t.Context(), url, `{"conversationId":"s","terminationReason":"NO_TOOL_CALL"}`, env, "hook", "stop", "--client", "antigravity")
			if r.code != exitOK || r.stdout != "" {
				t.Fatalf("unbounded continuation without state: %+v", r)
			}
		}
	})
	t.Run("unreachable server", func(t *testing.T) {
		r := runCLIInput(t.Context(), "http://127.0.0.1:1", `{"conversationId":"s","terminationReason":"NO_TOOL_CALL"}`, map[string]string{"AGORA_AGENT": "bob", "XDG_CACHE_HOME": t.TempDir()}, "hook", "stop", "--client", "antigravity")
		if r.code != exitOK || r.stdout != "" {
			t.Fatal(r)
		}
	})
	t.Run("unknown contract", func(t *testing.T) {
		r := runCLIInput(t.Context(), url, `{}`, nil, "hook", "stop", "--client", "unknown")
		if r.code != exitError {
			t.Fatal(r)
		}
	})
}
