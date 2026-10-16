package tui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/tone"
)

var update = flag.Bool("update", false, "rewrite golden files")

var now = time.Date(2026, 3, 4, 15, 30, 0, 0, time.UTC)

// at is a time minutes past the half hour before now.
func at(minutes int) time.Time { return now.Add(time.Duration(minutes-30) * time.Minute) }

func sampleConversation() core.Conversation {
	return core.Conversation{
		Namespace: "default", Name: "api-shape", Topic: "Pin the error envelope",
		End: core.EndAgreement, Status: core.StatusOpen, CreatedBy: "claude-implementer",
		CreatedAt: at(0), LastSeq: 12, LastAt: at(25),
		Participants: []core.Participant{
			{Agent: "claude-implementer", State: core.StateActive, Presence: core.PresenceWorking, LastSeenAt: at(25), YourAccount: true},
			{Agent: "codex-reviewer", State: core.StateConcluded, Presence: core.PresenceWaiting, Note: "waiting on the retry hint", LastSeenAt: at(24)},
			{Agent: "gemini-helper", State: core.StateActive, Presence: core.PresenceWorking, Stale: true, LastSeenAt: at(2)},
			{Agent: "old-bot", State: core.StateLeft, LastSeenAt: at(1)},
		},
		Observers: []core.Observer{{Name: "carol", Comments: 1, LastAt: at(10)}},
	}
}

func sampleMessages() []core.Message {
	three, eight := int64(3), int64(8)
	return []core.Message{
		{Seq: 1, Kind: core.KindSystem, Author: "claude-implementer", Body: "claude-implementer started the conversation (end: agreement)", CreatedAt: at(0)},
		{Seq: 2, Kind: core.KindSystem, Author: "codex-reviewer", Body: "codex-reviewer joined", CreatedAt: at(1)},
		{Seq: 3, Kind: core.KindSay, Author: "claude-implementer", Body: "Proposal: every error is `{code, message}`.\n\n- codes are **stable**\n- messages say what to do next\n\nSee [the spec](https://example.com/spec).", CreatedAt: at(2)},
		{Seq: 4, Kind: core.KindSay, Author: "claude-implementer", Body: "And `retry_after` on 429.", CreatedAt: at(2).Add(40 * time.Second)},
		{Seq: 9, Kind: core.KindObserver, Author: "carol", Body: "The CLI already retries on 503.", CreatedAt: at(10)},
		{Seq: 10, Kind: core.KindSay, Author: "codex-reviewer", Body: "Mostly.", CreatedAt: at(14)},
		{Seq: 11, Kind: core.KindSay, Author: "codex-reviewer", Body: "Agreed; `stale_context` should carry `last_seq`.\n\n```\nretry after: 2s\n```", ReplyTo: &three, SeenSeq: &eight, CreatedAt: at(20)},
		{Seq: 12, Kind: core.KindConclude, Author: "codex-reviewer", Body: "Envelope agreed.", CreatedAt: at(25)},
	}
}

// newTestModel returns a model with a fixed clock and size. Its client
// points nowhere; view tests never run the commands it returns.
func newTestModel(t *testing.T, opts Options, width, height int) *Model {
	t.Helper()
	if opts.Client == nil {
		c, err := client.New("http://127.0.0.1:1", "")
		if err != nil {
			t.Fatal(err)
		}
		opts.Client = c
	}
	opts.Skin = defaultSkin(t)
	opts.Now = func() time.Time { return now }
	opts.Location = time.UTC
	m := New(t.Context(), opts)
	m.Init()
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

// shot is the model's view without styling, trailing spaces trimmed.
func shot(m *Model) string {
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/tui -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s (run go test ./internal/tui -update and review the diff):\n%s", name, path, got)
	}
}

func openThread(t *testing.T, width, height int, observer string) *Model {
	m := newTestModel(t, Options{Namespace: "default", Conversation: "api-shape", Observer: observer}, width, height)
	conv := sampleConversation()
	m.Update(conversationMsg{gen: m.gen, conv: conv})
	m.Update(followMsg{gen: m.gen, res: client.FollowResult{
		ReadResult:   core.ReadResult{Messages: sampleMessages(), LastSeq: 12},
		Conversation: conv,
	}})
	return m
}

func TestViews(t *testing.T) {
	t.Run("conversation wide", func(t *testing.T) {
		golden(t, "conversation-wide", shot(openThread(t, 110, 40, "alice")))
	})
	t.Run("conversation narrow", func(t *testing.T) {
		golden(t, "conversation-narrow", shot(openThread(t, 60, 40, "")))
	})
	t.Run("participants overlay", func(t *testing.T) {
		m := openThread(t, 60, 24, "")
		m.Update(press("p"))
		golden(t, "participants-narrow", shot(m))
	})
	t.Run("concluded", func(t *testing.T) {
		m := openThread(t, 80, 20, "alice")
		conv := sampleConversation()
		conv.Status = core.StatusConcluded
		m.Update(followMsg{gen: m.gen, res: client.FollowResult{ReadResult: core.ReadResult{LastSeq: 12}, Conversation: conv}})
		golden(t, "conversation-concluded", shot(m))
	})
	// A message that arrives later joins the run of the one before it.
	t.Run("live run", func(t *testing.T) {
		m := openThread(t, 80, 60, "")
		conv := sampleConversation()
		conv.LastSeq = 14
		m.Update(followMsg{gen: m.gen, res: client.FollowResult{
			ReadResult: core.ReadResult{Messages: []core.Message{
				{Seq: 13, Kind: core.KindSay, Author: "gemini-helper", Body: "late one", CreatedAt: at(26)},
			}, LastSeq: 13},
			Conversation: conv,
		}})
		m.Update(followMsg{gen: m.gen, res: client.FollowResult{
			ReadResult: core.ReadResult{Messages: []core.Message{
				{Seq: 14, Kind: core.KindSay, Author: "gemini-helper", Body: "late two", CreatedAt: at(29)},
			}, LastSeq: 14},
			Conversation: conv,
		}})
		got := shot(m)
		if !strings.Contains(got, "late one\n#14 ▎ +3m\n    ▎ late two\n") {
			t.Fatalf("no run:\n%s", got)
		}
	})
	// A second conclusion from the same author continues the first and
	// keeps its tag.
	t.Run("conclusion run", func(t *testing.T) {
		m := openThread(t, 80, 60, "")
		conv := sampleConversation()
		conv.LastSeq = 13
		m.Update(followMsg{gen: m.gen, res: client.FollowResult{
			ReadResult: core.ReadResult{Messages: []core.Message{
				{Seq: 13, Kind: core.KindConclude, Author: "codex-reviewer", Body: "Envelope agreed, again.", CreatedAt: at(26)},
			}, LastSeq: 13},
			Conversation: conv,
		}})
		got := shot(m)
		if !strings.Contains(got, "Envelope agreed.\n#13 ▎ +1m  concluded\n") {
			t.Fatalf("no run:\n%s", got)
		}
	})
	t.Run("conversation list", func(t *testing.T) {
		m := newTestModel(t, Options{Namespace: "default"}, 90, 10)
		c1, c2 := sampleConversation(), sampleConversation()
		c2.Name, c2.Topic, c2.Status, c2.Archived, c2.Participants = "naming", "", core.StatusConcluded, true, nil
		m.Update(conversationsMsg{gen: m.gen, page: core.ListPage{Conversations: []core.Conversation{c1, c2}}})
		golden(t, "conversations", shot(m))
	})
}

func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "shift+enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
	case "ctrl+j":
		return tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	r := []rune(s)
	if len(r) != 1 {
		panic("press: " + s)
	}
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func TestViewFitsTheWindow(t *testing.T) {
	multiline := sampleConversation()
	multiline.Topic = "first\nsecond\nthird"
	for _, size := range [][2]int{{40, 12}, {60, 24}, {99, 30}, {100, 30}, {160, 50}} {
		m := openThread(t, size[0], size[1], "alice")
		m.Update(followMsg{gen: m.gen, res: client.FollowResult{ReadResult: core.ReadResult{LastSeq: 12}, Conversation: multiline}})
		checkFits(t, m, size[0], size[1])
		list := newTestModel(t, Options{Namespace: "default"}, size[0], size[1])
		list.Update(conversationsMsg{gen: list.gen, page: core.ListPage{Conversations: []core.Conversation{multiline}}})
		checkFits(t, list, size[0], size[1])
	}
}

func checkFits(t *testing.T, m *Model, width, height int) {
	t.Helper()
	{
		size := [2]int{width, height}
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > size[0] {
				t.Errorf("%dx%d: line %d is %d wide: %q", size[0], size[1], i, w, ansi.Strip(l))
			}
		}
	}
}

// TestShrinkKeepsFollowing checks that a view at the bottom still follows
// new messages after the window gets shorter.
func TestShrinkKeepsFollowing(t *testing.T) {
	m := openThread(t, 110, 60, "alice")
	var more []core.Message
	for i := range 30 {
		more = append(more, core.Message{Seq: int64(13 + i), Kind: core.KindSay, Author: "codex-reviewer", Body: "line", CreatedAt: at(26)})
	}
	conv := sampleConversation()
	m.Update(followMsg{gen: m.gen, res: client.FollowResult{ReadResult: core.ReadResult{Messages: more, LastSeq: 42}, Conversation: conv}})
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
	m.Update(followMsg{gen: m.gen, res: client.FollowResult{ReadResult: core.ReadResult{
		Messages: []core.Message{{Seq: 43, Kind: core.KindSay, Author: "claude-implementer", Body: "after the resize", CreatedAt: at(27)}},
		LastSeq:  43,
	}, Conversation: conv}})
	if m.t.newBelow != 0 || !strings.Contains(shot(m), "after the resize") {
		t.Fatalf("not following after a shrink (new below: %d):\n%s", m.t.newBelow, shot(m))
	}
}

// TestParticipantColors checks that an author's name and bar use the tone
// the HTML viewer gives the same name.
func TestParticipantColors(t *testing.T) {
	m := openThread(t, 110, 40, "alice")
	sk := defaultSkin(t)
	for _, name := range []string{"claude-implementer", "codex-reviewer"} {
		r, g, b, _ := sk.dark[fmt.Sprintf("tone_%d", tone.Of(name))].RGBA()
		sgr := fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
		found := false
		for l := range strings.SplitSeq(m.View().Content, "\n") {
			if strings.Contains(ansi.Strip(l), name+"  ") && strings.Contains(l, sgr) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not drawn in its tone (%s)", name, sgr)
		}
	}
}
