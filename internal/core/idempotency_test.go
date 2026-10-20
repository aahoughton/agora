package core_test

import (
	"testing"

	"github.com/aahoughton/smoot/internal/core"
)

func TestIdempotencyKeyCoversTheReplyTarget(t *testing.T) {
	f := newFixture(t, core.Options{})
	f.start(t, "c", core.EndNone, "a")
	say := func(reply *int64) error {
		_, err := f.svc.Say(t.Context(), f.p, "default", "c", core.SayParams{Agent: "a", Body: "yes", ReplyTo: reply, IdempotencyKey: "k"})
		return err
	}
	if err := say(ptr[int64](1)); err != nil {
		t.Fatal(err)
	}
	if err := say(ptr[int64](1)); err != nil {
		t.Fatalf("same retry: %v", err)
	}
	for _, reply := range []*int64{ptr[int64](2), nil} {
		wantCode(t, say(reply), core.CodeIdempotencyConflict)
	}
}
