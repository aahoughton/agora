package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aahoughton/agora/internal/core"
)

func TestMentionPrefix(t *testing.T) {
	for _, tc := range []struct {
		before, want string
		ok           bool
	}{
		{"@", "", true},
		{"@cod", "cod", true},
		{"ask @codex-re", "codex-re", true},
		{"(@cl", "cl", true},
		{"ask codex", "", false},
		{"me@example", "", false},
		{"@a @b", "b", true},
		{"@cod ", "", false},
	} {
		t.Run(tc.before, func(t *testing.T) {
			got, ok := mentionPrefix([]rune(tc.before))
			if got != tc.want || ok != tc.ok {
				t.Fatalf("mentionPrefix(%q) = %q, %v; want %q, %v", tc.before, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestMentionMatches(t *testing.T) {
	names := []string{"codex-reviewer", "claude-impl", "Codex-helper", "codex-reviewer", "carol"}
	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"", []string{"codex-reviewer", "claude-impl", "Codex-helper", "carol"}},
		{"co", []string{"codex-reviewer", "Codex-helper"}},
		{"CL", []string{"claude-impl"}},
		{"z", nil},
	} {
		t.Run(tc.prefix, func(t *testing.T) {
			if got := mentionMatches(tc.prefix, names); !slices.Equal(got, tc.want) {
				t.Fatalf("mentionMatches(%q) = %q, want %q", tc.prefix, got, tc.want)
			}
		})
	}
}

// mentionHarness opens conversation design, with participants ca-one and
// cb-two and observer cora, in the composer.
func mentionHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, Options{Namespace: "default", Conversation: "design", Observer: "carol"})
	ctx, p := t.Context(), core.Anonymous()
	if _, err := h.svc.Start(ctx, p, "default", core.StartParams{Name: "design", End: core.EndAgreement, Agent: "ca-one", Join: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Join(ctx, p, "default", "design", core.JoinParams{Agent: "cb-two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Observe(ctx, p, "default", "design", core.ObserveParams{Name: "cora", Body: "worth a look"}); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.until("the observer comment", h.shows("worth a look"))
	h.keys("i")
	return h
}

func (h *harness) composerIs(want string) {
	h.t.Helper()
	if got := h.m.t.composer.Value(); got != want {
		h.t.Fatalf("composer = %q, want %q", got, want)
	}
}

func TestComposerCompletesMentions(t *testing.T) {
	h := mentionHarness(t)
	value := h.composerIs
	h.typeText("ask @c")
	// Matches come in panel order: participants (by join time, then name),
	// then observers.
	h.keys("tab")
	value("ask @ca-one ")
	h.keys("tab")
	value("ask @cb-two ")
	h.keys("tab")
	value("ask @cora ")
	h.keys("tab")
	value("ask @ca-one ")
	// Typing ends the cycle; tab with no @name before the cursor does nothing.
	h.typeText("and ")
	h.keys("tab")
	value("ask @ca-one and ")
	h.typeText("@CB")
	h.keys("tab")
	value("ask @ca-one and @cb-two ")
	h.keys("ctrl+j")
	h.typeText("@cor")
	h.keys("tab")
	value("ask @ca-one and @cb-two \n@cora ")
}

func TestComposerMentionEdgeCases(t *testing.T) {
	left := tea.KeyPressMsg{Code: tea.KeyLeft}
	t.Run("a paste ends the cycle", func(t *testing.T) {
		h := mentionHarness(t)
		h.typeText("ask @c")
		h.keys("tab")
		h.send(tea.PasteMsg{Content: "hello"})
		h.keys("tab")
		h.composerIs("ask @ca-one hello")
	})
	t.Run("a selection is dropped, not deleted", func(t *testing.T) {
		h := mentionHarness(t)
		h.typeText("ask @cbxy")
		h.send(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
		h.send(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
		h.keys("tab")
		h.composerIs("ask @cb-two xy")
	})
	t.Run("mid-line keeps one space", func(t *testing.T) {
		h := mentionHarness(t)
		h.typeText("ask @c please")
		for range len(" please") {
			h.send(left)
		}
		h.keys("tab")
		h.composerIs("ask @ca-one please")
		h.keys("tab")
		h.composerIs("ask @cb-two please")
		h.typeText("!")
		h.composerIs("ask @cb-two! please")
	})
	t.Run("one match does not cycle", func(t *testing.T) {
		h := mentionHarness(t)
		h.typeText("@cor")
		h.keys("tab")
		h.composerIs("@cora ")
		h.keys("tab")
		h.composerIs("@cora ")
		if h.m.t.mention != nil {
			t.Fatal("the cycle is still open after a tab on one match")
		}
	})
}

func TestMentionsRecolorWhenSomeoneJoins(t *testing.T) {
	h := newHarness(t, Options{Namespace: "default", Conversation: "design"})
	ctx, p := t.Context(), core.Anonymous()
	if _, err := h.svc.Start(ctx, p, "default", core.StartParams{Name: "design", End: core.EndAgreement, Agent: "alice", Join: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Say(ctx, p, "default", "design", core.SayParams{Agent: "alice", Body: "waiting on @late"}); err != nil {
		t.Fatal(err)
	}
	h.start()
	h.until("the message", h.shows("waiting on @late"))
	styled := words("@late", lipgloss.NewStyle().Foreground(h.m.st.toneOf("late").GetForeground()).Bold(true))
	shown := func() bool { return strings.Contains(h.m.View().Content, styled) }
	if shown() {
		t.Fatal("@late is a mention before late joins")
	}
	if _, err := h.svc.Join(ctx, p, "default", "design", core.JoinParams{Agent: "late"}); err != nil {
		t.Fatal(err)
	}
	h.until("@late in late's color", shown)
}
