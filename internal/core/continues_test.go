package core_test

import (
	"testing"

	"github.com/aahoughton/smoot/internal/core"
)

func TestContinues(t *testing.T) {
	msg := func(kind, author string) core.Message { return core.Message{Kind: kind, Author: author} }
	for _, tc := range []struct {
		name string
		prev *core.Message
		m    core.Message
		want bool
	}{
		{"first message", nil, msg(core.KindSay, "alice"), false},
		{"same author says", ptr(msg(core.KindSay, "alice")), msg(core.KindSay, "alice"), true},
		{"other author", ptr(msg(core.KindSay, "alice")), msg(core.KindSay, "bob"), false},
		{"say then conclude", ptr(msg(core.KindSay, "alice")), msg(core.KindConclude, "alice"), false},
		{"conclude then say", ptr(msg(core.KindConclude, "alice")), msg(core.KindSay, "alice"), false},
		{"conclude twice", ptr(msg(core.KindConclude, "alice")), msg(core.KindConclude, "alice"), true},
		{"observer twice", ptr(msg(core.KindObserver, "pat")), msg(core.KindObserver, "pat"), true},
		{"after a system line", ptr(msg(core.KindSystem, "alice")), msg(core.KindSay, "alice"), false},
		{"system lines", ptr(msg(core.KindSystem, "smoot")), msg(core.KindSystem, "smoot"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := core.Continues(tc.prev, tc.m); got != tc.want {
				t.Errorf("Continues = %v, want %v", got, tc.want)
			}
		})
	}
}
