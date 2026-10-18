package api

import (
	"net/http"
	"time"

	"github.com/aahoughton/agora/internal/core"
)

func (h *handler) search(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	q := r.URL.Query()
	sp := core.SearchParams{
		Query: q.Get("q"), Conversation: q.Get("conversation"), Author: q.Get("author"),
		Order: q.Get("order"), Cursor: q.Get("cursor"),
	}
	for name, dst := range map[string]**time.Time{"since": &sp.Since, "until": &sp.Until} {
		if s := q.Get(name); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return invalid("query parameter %s must be an RFC 3339 time", name)
			}
			*dst = &t
		}
	}
	limit, err := queryCount(r, "limit")
	if err != nil {
		return err
	}
	if limit != nil {
		if *limit < 1 || *limit > core.MaxSearchLimit {
			return invalid("limit must be between 1 and %d", core.MaxSearchLimit)
		}
		sp.Limit = *limit
	}
	page, err := h.svc.Search(r.Context(), p, r.PathValue("ns"), sp)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page)
	return nil
}
