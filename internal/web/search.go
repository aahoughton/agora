package web

import (
	"net/http"
	"strconv"

	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/markdown"
)

// SearchResults is a page of search hits on /n/{namespace}/search.
type SearchResults struct {
	// Total counts every matching message, on all pages.
	Total int
	Hits  []SearchHit
	// Next continues the search (as ?cursor=, with the same q and order);
	// empty on the last page.
	Next string
}

// SearchHit is one matching message.
type SearchHit struct {
	core.SearchHit
	// Link opens the conversation at the message.
	Link string
}

// hitContext is how many messages after a hit its conversation link shows
// at most, so the hit sits near the end of a page of earlier messages.
const hitContext = 20

// hitLink opens the conversation at the hit's message, with up to
// hitContext messages after it but never more than half of the shown
// messages, so the hit stays on the page. A ?before= past the last message
// shows the live page.
func hitLink(ns string, h core.SearchHit, shown int) string {
	after := int64(min(hitContext, shown/2))
	return "/n/" + ns + "/c/" + h.Conversation + "?before=" + strconv.FormatInt(h.Seq+after+1, 10) +
		"#m" + strconv.FormatInt(h.Seq, 10)
}

func (v *viewer) search(w http.ResponseWriter, r *http.Request) {
	p, account, ok := v.principal(w, r)
	if !ok {
		return
	}
	ns := r.PathValue("ns")
	q := r.URL.Query()
	data := Page{Title: ns, Namespace: ns, Account: account, Query: q.Get("q"), Order: q.Get("order")}
	if data.Order == "" {
		data.Order = core.OrderRelevance
	}
	if data.Query == "" {
		v.render(w, http.StatusOK, "search", data)
		return
	}
	page, err := v.svc.Search(r.Context(), p, ns, core.SearchParams{Query: data.Query, Order: data.Order, Cursor: q.Get("cursor")})
	switch core.CodeOf(err) {
	case "":
	case core.CodeInvalid:
		// A query the server cannot use is reported beside the search box.
		data.Notice = err.Error()
		v.render(w, http.StatusBadRequest, "search", data)
		return
	default:
		v.fail(w, err, data)
		return
	}
	data.Search = &SearchResults{Total: page.Total, Next: page.Next, Hits: make([]SearchHit, len(page.Hits))}
	for i, h := range page.Hits {
		for j := range h.Snippet {
			h.Snippet[j].Text = markdown.StripBidi(h.Snippet[j].Text)
		}
		data.Search.Hits[i] = SearchHit{SearchHit: h, Link: hitLink(ns, h, v.opts.ShownMessages)}
	}
	v.render(w, http.StatusOK, "search", data)
}
