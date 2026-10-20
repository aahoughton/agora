package web_test

import (
	"bufio"
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/smoot/internal/core"
	"github.com/aahoughton/smoot/internal/web"
)

// seedRuns starts "runs", whose messages are, by seq:
//
//	1  system   alice started the conversation
//	2  system   alice joined
//	3  say      alice
//	4  say      alice, 4s later
//	5  say      alice, 6m later, re #3
//	6  system   bob joined
//	7  say      alice
//	8  say      bob
//	9  say      bob, 2h later
//	10 observer pat
//	11 observer pat
//	12 conclude bob
//	13 conclude bob, 30s later
func (e *env) seedRuns() {
	e.t.Helper()
	ctx, p := e.t.Context(), core.Anonymous()
	must(e.t, err1(e.svc.Start(ctx, p, "default", core.StartParams{Name: "runs", Topic: "runs", Agent: "alice", Join: true})))
	say := func(agent, body string, after time.Duration, reply *int64) {
		e.t.Helper()
		e.advance(after)
		must(e.t, err1(e.svc.Say(ctx, p, "default", "runs", core.SayParams{Agent: agent, Body: body, ReplyTo: reply})))
	}
	say("alice", "first", time.Second, nil)
	say("alice", "second", 4*time.Second, nil)
	say("alice", "third", 6*time.Minute, ptr(int64(3)))
	must(e.t, err1(e.svc.Join(ctx, p, "default", "runs", core.JoinParams{Agent: "bob"})))
	say("alice", "after the join", time.Second, nil)
	say("bob", "bob one", time.Second, nil)
	say("bob", "bob two", 2*time.Hour, nil)
	for _, body := range []string{"pat one", "pat two"} {
		must(e.t, err1(e.svc.Observe(ctx, p, "default", "runs", core.ObserveParams{Name: "pat", Body: body})))
	}
	must(e.t, err1(e.svc.Conclude(ctx, p, "default", "runs", core.ConcludeParams{Agent: "bob", Body: "AGREED"})))
	e.advance(30 * time.Second)
	must(e.t, err1(e.svc.Conclude(ctx, p, "default", "runs", core.ConcludeParams{Agent: "bob", Body: "AGREED again"})))
}

func err1[T any](_ T, err error) error { return err }

var messageTag = regexp.MustCompile(`<li class="message ([^"]*)" id="m(\d+)"`)

// continuing returns the seqs of the messages marked as continuing a run.
func continuing(html string) string {
	var out []string
	for _, m := range messageTag.FindAllStringSubmatch(html, -1) {
		if strings.Contains(" "+m[1]+" ", " continues ") {
			out = append(out, m[2])
		}
	}
	return strings.Join(out, ",")
}

func TestMessageRuns(t *testing.T) {
	e := newEnv(t, web.Options{})
	e.seedRuns()
	_, body := e.get("/n/default/c/runs")
	if got := continuing(body); got != "4,5,9,11,13" {
		t.Fatalf("continuing = %q, want 4,5,9,11,13", got)
	}
	for _, want := range []string{">+4s</time>", ">+6m</time>", ">+2h</time>", ">+30s</time> <span class=\"tag\">conclusion</span>", `<a class="reply" href="#m3">re #3</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	// A continuing message drops the author; each message keeps its seq.
	if n := strings.Count(body, `<span class="author">alice</span>`); n != 2 {
		t.Errorf("alice has %d author headers, want 2", n)
	}
	if n := len(messageTag.FindAllString(body, -1)); n != 13 {
		t.Errorf("%d messages, want 13", n)
	}
}

// The first message on a page names its author, even when it continues a
// run from the page before.
func TestMessageRunsAcrossPages(t *testing.T) {
	e := newEnv(t, web.Options{ShownMessages: 2})
	e.seedRuns()
	_, body := e.get("/n/default/c/runs?before=6")
	if got := continuing(body); got != "5" {
		t.Fatalf("continuing = %q, want 5", got)
	}
	if !strings.Contains(body, `<span class="author">alice</span>`) {
		t.Fatal("first message on the page has no author")
	}
}

// A live message continues the last one the page has.
func TestMessageRunsLive(t *testing.T) {
	e := newEnv(t, web.Options{})
	e.seedRuns()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.url+"/n/default/c/runs/events?after=8", nil)
	resp, err := e.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	// Seq 9 continues 8 (bob), and 11 continues 10 (pat).
	var got []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && len(got) < 4 {
		line := sc.Text()
		if m := messageTag.FindStringSubmatch(line); m != nil {
			got = append(got, m[2]+":"+continuing(line))
		}
	}
	if strings.Join(got, " ") != "9:9 10: 11:11 12:" {
		t.Fatalf("live messages = %q", got)
	}
}
