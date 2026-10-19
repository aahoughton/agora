package web_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aahoughton/smoot/internal/core"
	"github.com/aahoughton/smoot/internal/web"
)

func TestSearchPage(t *testing.T) {
	e := newEnv(t, web.Options{ShownMessages: 5})
	e.seed()
	ctx, p := t.Context(), core.Anonymous()
	for i := range 30 {
		if _, err := e.svc.Say(ctx, p, "default", "cache", core.SayParams{Agent: "bob", Body: fmt.Sprintf("filler %d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	// The namespace page offers the search box.
	if _, body := e.get("/n/default"); !strings.Contains(body, `action="/n/default/search"`) {
		t.Fatal("no search box on the namespace page")
	}

	status, body := e.get("/n/default/search?q=lru")
	if status != http.StatusOK || !strings.Contains(body, "3 messages match") {
		t.Fatalf("search = %d:\n%s", status, body)
	}
	// Matches are marked, and markup in a body is escaped, not rendered.
	if !strings.Contains(body, "<mark>LRU</mark>") || strings.Contains(body, "<script>alert") {
		t.Fatalf("snippet markup:\n%s", body)
	}
	// A hit links to a page that shows it, even one older than the latest
	// page.
	// With 5 messages to a page, the hit at #4 gets 2 after it.
	const link = `href="/n/default/c/cache?before=7#m4"`
	if !strings.Contains(body, link) {
		t.Fatalf("no link %s in:\n%s", link, body)
	}
	if _, conv := e.get("/n/default/c/cache?before=7"); !strings.Contains(conv, `id="m4"`) {
		t.Fatal("the linked page does not show the hit")
	}

	// Paging keeps the query and order.
	status, body = e.get("/n/default/search?q=filler&order=oldest")
	if status != http.StatusOK || !strings.Contains(body, "30 messages match") || !strings.Contains(body, `href="?q=filler&order=oldest&cursor=o`) {
		t.Fatalf("paged search = %d:\n%s", status, body)
	}

	// A query with spaces and quotes survives the round trip to the next
	// page.
	if _, body := e.get("/n/default/search?q=%22filler+1%22+filler&limit=1"); !strings.Contains(body, `value="&#34;filler 1&#34; filler"`) {
		t.Fatalf("query not kept in the box:\n%s", body)
	}

	// A query the server cannot use is reported beside the box.
	status, body = e.get("/n/default/search?q=%22%22")
	if status != http.StatusBadRequest || !strings.Contains(body, "query has no words to search for") || !strings.Contains(body, `name="q"`) {
		t.Fatalf("bad query = %d:\n%s", status, body)
	}
	if status, _ := e.get("/n/nowhere/search?q=x"); status != http.StatusNotFound {
		t.Fatalf("missing namespace = %d", status)
	}
}
