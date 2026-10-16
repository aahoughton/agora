package web_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/web"
)

// seedCrossed starts "x", where alice posts #6 without having read bob's
// #4 and #5:
//
//	1 started, 2 alice joined, 3 bob joined (alice reads up to here)
//	4 bob b1, 5 bob b2, 6 alice a1
func (e *env) seedCrossed() {
	e.t.Helper()
	ctx, p := e.t.Context(), core.Anonymous()
	must(e.t, err1(e.svc.Start(ctx, p, "default", core.StartParams{Name: "x", Agent: "alice", Join: true})))
	must(e.t, err1(e.svc.Join(ctx, p, "default", "x", core.JoinParams{Agent: "bob"})))
	must(e.t, err1(e.svc.Read(ctx, p, "default", "x", core.ReadParams{Agent: "alice"})))
	for _, s := range []core.SayParams{{Agent: "bob", Body: "b1"}, {Agent: "bob", Body: "b2"}, {Agent: "alice", Body: "a1"}} {
		must(e.t, err1(e.svc.Say(ctx, p, "default", "x", s)))
	}
}

const missed45 = `missed <a href="#m4">#4</a>, <a href="#m5">#5</a></span>`

// A message is marked with what its author missed, read from the store
// when those messages are not on the page.
func TestUnseenMarked(t *testing.T) {
	e := newEnv(t, web.Options{ShownMessages: 1})
	e.seedCrossed()
	_, body := e.get("/n/default/c/x")
	if !strings.Contains(body, `id="m6"`) || strings.Contains(body, `id="m5"`) {
		t.Fatal("page does not show only #6")
	}
	if strings.Count(body, `class="unseen"`) != 1 || !strings.Contains(body, missed45) {
		t.Fatalf("no missed marker on #6:\n%s", body)
	}
}

// A live message is marked too, though what it missed came before the
// stream started.
func TestUnseenLive(t *testing.T) {
	e := newEnv(t, web.Options{})
	e.seedCrossed()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.url+"/n/default/c/x/events?after=6", nil)
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	go func() {
		_, _ = e.svc.Say(context.Background(), core.Anonymous(), "default", "x", core.SayParams{Agent: "alice", Body: "a2"})
	}()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if line := sc.Text(); strings.Contains(line, `id="m7"`) {
			for sc.Scan() && !strings.Contains(sc.Text(), "class=\"unseen\"") {
				if sc.Text() == "" {
					t.Fatal("live #7 has no missed marker")
				}
			}
			if !strings.Contains(sc.Text(), missed45) {
				t.Fatalf("live #7 marker: %s", sc.Text())
			}
			return
		}
	}
	t.Fatal("stream ended before #7")
}
