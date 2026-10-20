package core_test

import (
	"strings"
	"testing"

	"github.com/aahoughton/smoot/internal/core"
)

func TestReadsCanBeBoundedByBytes(t *testing.T) {
	f := newFixture(t, core.Options{})
	f.start(t, "c", core.EndNone, "a", "b")
	must[core.ReadResult](t)(f.svc.Read(t.Context(), f.p, "default", "c", core.ReadParams{Agent: "b"}))
	big := strings.Repeat("x", 40<<10)
	for range 3 {
		f.say(t, "c", "a", big)
	}
	read := func() core.ReadResult {
		return must[core.ReadResult](t)(f.svc.Read(t.Context(), f.p, "default", "c", core.ReadParams{Agent: "b", MaxBytes: 50 << 10}))
	}
	first := read()
	if len(first.Messages) != 1 || !first.More || first.Standing.Unread != 2 {
		t.Fatalf("first page: %d messages, more %v, standing %+v", len(first.Messages), first.More, first.Standing)
	}
	second := read()
	if len(second.Messages) != 1 || second.Messages[0].Seq != first.Messages[0].Seq+1 {
		t.Fatalf("second page starts at %+v", second.Messages)
	}
}
