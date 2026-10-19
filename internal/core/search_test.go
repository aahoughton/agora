package core_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/smoot/internal/core"
)

// searchFixture holds two conversations in default and one in another
// namespace, a minute apart per message.
func searchFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, core.Options{})
	f.start(t, "plan", core.EndAgreement, "alice", "bob")
	f.clock.Advance(time.Minute)
	f.say(t, "plan", "alice", "Shall we migrate the messages table?")
	f.clock.Advance(time.Minute)
	f.say(t, "plan", "bob", "The migration needs a backup first.\nThen a rehearsal.")
	f.clock.Advance(time.Minute)
	must[core.Message](t)(f.svc.Observe(t.Context(), f.p, "default", "plan", core.ObserveParams{Name: "pat", Body: "Migration plan looks fine"}))
	f.clock.Advance(time.Minute)
	f.start(t, "other", core.EndNone, "carol")
	f.clock.Advance(time.Minute)
	f.say(t, "other", "carol", "An unrelated migration, done with seen_seq in mind.")
	must[core.Namespace](t)(f.svc.CreateNamespace(t.Context(), f.p, "elsewhere"))
	must[core.Conversation](t)(f.svc.Start(t.Context(), f.p, "elsewhere", core.StartParams{Name: "x", Agent: "dan", Join: true}))
	must[core.SayResult](t)(f.svc.Say(t.Context(), f.p, "elsewhere", "x", core.SayParams{Agent: "dan", Body: "a migration elsewhere"}))
	return f
}

func hitKeys(page core.SearchPage) []string {
	var keys []string
	for _, h := range page.Hits {
		keys = append(keys, h.Conversation+"#"+strings.TrimSpace(h.Author))
	}
	return keys
}

func TestSearch(t *testing.T) {
	f := searchFixture(t)
	search := func(sp core.SearchParams) core.SearchPage {
		t.Helper()
		return must[core.SearchPage](t)(f.svc.Search(t.Context(), f.p, "default", sp))
	}
	tests := []struct {
		name string
		sp   core.SearchParams
		want []string
	}{
		{"whole words, case-folded, one namespace", core.SearchParams{Query: "migration", Order: core.OrderOldest}, []string{"plan#bob", "plan#pat", "other#carol"}},
		{"no stemming", core.SearchParams{Query: "migrate"}, []string{"plan#alice"}},
		{"prefix", core.SearchParams{Query: "migrat*", Order: core.OrderOldest}, []string{"plan#alice", "plan#bob", "plan#pat", "other#carol"}},
		{"words are ANDed", core.SearchParams{Query: "migration backup"}, []string{"plan#bob"}},
		{"phrase", core.SearchParams{Query: `"migration plan"`}, []string{"plan#pat"}},
		{"phrase in order only", core.SearchParams{Query: `"plan migration"`}, nil},
		{"identifier as a phrase", core.SearchParams{Query: "seen_seq"}, []string{"other#carol"}},
		{"system lines are not searched", core.SearchParams{Query: "joined"}, nil},
		{"conversation filter", core.SearchParams{Query: "migration", Conversation: "other"}, []string{"other#carol"}},
		{"author filter", core.SearchParams{Query: "migration", Author: "pat"}, []string{"plan#pat"}},
		{"since", core.SearchParams{Query: "migration", Since: ptr(f.clock.Now().Add(-90 * time.Second)), Order: core.OrderOldest}, []string{"other#carol"}},
		{"until", core.SearchParams{Query: "migration", Until: ptr(f.clock.Now().Add(-150 * time.Second)), Order: core.OrderOldest}, []string{"plan#bob"}},
		{"newest first", core.SearchParams{Query: "migration", Order: core.OrderNewest}, []string{"other#carol", "plan#pat", "plan#bob"}},
		{"FTS5 syntax is just words", core.SearchParams{Query: `migration OR NEAR(x) body:"`}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page := search(tt.sp)
			if got := hitKeys(page); !slices.Equal(got, tt.want) {
				t.Fatalf("hits = %v, want %v", got, tt.want)
			}
			if page.Total != len(tt.want) {
				t.Fatalf("total = %d, want %d", page.Total, len(tt.want))
			}
		})
	}
}

func TestSearchHitAndSnippet(t *testing.T) {
	f := searchFixture(t)
	page := must[core.SearchPage](t)(f.svc.Search(t.Context(), f.p, "default", core.SearchParams{Query: "backup"}))
	if len(page.Hits) != 1 {
		t.Fatalf("hits = %+v", page.Hits)
	}
	h := page.Hits[0]
	if h.Conversation != "plan" || h.Seq != 5 || h.Kind != core.KindSay || h.Author != "bob" || !h.CreatedAt.Equal(time.Date(2030, 1, 2, 3, 6, 5, 0, time.UTC)) {
		t.Fatalf("hit = %+v", h)
	}
	// One line, with the matched word marked.
	if got := h.Text(); got != "The migration needs a backup first. Then a rehearsal." {
		t.Fatalf("snippet text = %q", got)
	}
	var matched []string
	for _, p := range h.Snippet {
		if p.Match {
			matched = append(matched, p.Text)
		}
	}
	if !slices.Equal(matched, []string{"backup"}) {
		t.Fatalf("matched parts = %v in %+v", matched, h.Snippet)
	}
}

// Paging walks every match once: by offset in relevance order, and by
// position in time order, where a message arriving between pages lands
// after the walk (oldest) or before it (newest) instead of shifting it.
func TestSearchPaging(t *testing.T) {
	for _, order := range []string{core.OrderRelevance, core.OrderNewest, core.OrderOldest} {
		t.Run(order, func(t *testing.T) {
			f := newFixture(t, core.Options{})
			f.start(t, "c", core.EndNone, "alice")
			var want []int64
			for range 7 {
				f.clock.Advance(time.Second)
				want = append(want, f.say(t, "c", "alice", "another note").Message.Seq)
			}
			var seqs []int64
			cursor := ""
			for page := 0; ; page++ {
				res := must[core.SearchPage](t)(f.svc.Search(t.Context(), f.p, "default", core.SearchParams{Query: "note", Order: order, Limit: 3, Cursor: cursor}))
				for _, h := range res.Hits {
					seqs = append(seqs, h.Seq)
				}
				if page == 0 && order != core.OrderRelevance {
					f.clock.Advance(time.Second)
					late := f.say(t, "c", "alice", "a late note").Message.Seq
					if order == core.OrderOldest {
						want = append(want, late)
					}
				}
				if cursor = res.Next; cursor == "" || page > 5 {
					break
				}
			}
			slices.Sort(seqs)
			if !slices.Equal(seqs, want) {
				t.Fatalf("seqs = %v, want each of %v once", seqs, want)
			}
		})
	}
}

func TestSearchOrder(t *testing.T) {
	f := newFixture(t, core.Options{})
	f.start(t, "c", core.EndNone, "alice")
	var seqs []int64
	for _, body := range []string{"note", "note note note", "a note among many other words here"} {
		f.clock.Advance(time.Second)
		seqs = append(seqs, f.say(t, "c", "alice", body).Message.Seq)
	}
	got := func(order string) []int64 {
		var out []int64
		for _, h := range must[core.SearchPage](t)(f.svc.Search(t.Context(), f.p, "default", core.SearchParams{Query: "note", Order: order})).Hits {
			out = append(out, h.Seq)
		}
		return out
	}
	if g := got(core.OrderOldest); !slices.Equal(g, seqs) {
		t.Errorf("oldest = %v, want %v", g, seqs)
	}
	if g := got(core.OrderNewest); !slices.Equal(g, []int64{seqs[2], seqs[1], seqs[0]}) {
		t.Errorf("newest = %v", g)
	}
	// BM25 ranks the message most about the word first and the one where
	// it is diluted last.
	if g := got(core.OrderRelevance); !slices.Equal(g, []int64{seqs[1], seqs[0], seqs[2]}) {
		t.Errorf("relevance = %v, want %v", g, []int64{seqs[1], seqs[0], seqs[2]})
	}
}

func TestSearchRejects(t *testing.T) {
	f := searchFixture(t)
	tests := []struct {
		name string
		sp   core.SearchParams
		code core.Code
	}{
		{"empty", core.SearchParams{Query: "  "}, core.CodeInvalid},
		{"punctuation only", core.SearchParams{Query: `"" ** !!`}, core.CodeInvalid},
		{"NUL", core.SearchParams{Query: "migration\x00backup"}, core.CodeInvalid},
		{"too long", core.SearchParams{Query: strings.Repeat("a", core.MaxQueryBytes+1)}, core.CodeInvalid},
		{"too many terms", core.SearchParams{Query: strings.Repeat("w ", 21)}, core.CodeInvalid},
		{"limit", core.SearchParams{Query: "x", Limit: core.MaxSearchLimit + 1}, core.CodeInvalid},
		{"order", core.SearchParams{Query: "x", Order: "alphabetical"}, core.CodeInvalid},
		{"cursor from another order", core.SearchParams{Query: "x", Order: core.OrderNewest, Cursor: "r3"}, core.CodeInvalid},
		{"garbled cursor", core.SearchParams{Query: "x", Cursor: "rx"}, core.CodeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.svc.Search(t.Context(), f.p, "default", tt.sp)
			wantCode(t, err, tt.code)
		})
	}
	_, err := f.svc.Search(t.Context(), f.p, "nowhere", core.SearchParams{Query: "x"})
	wantCode(t, err, core.CodeNamespaceNotFound)
}
