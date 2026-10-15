package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/aahoughton/agora/internal/core"
)

// Agents submit feedback over MCP; server admins read it here.

func (h *handler) listFeedback(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	q := r.URL.Query()
	f := core.FeedbackFilter{Namespace: q.Get("namespace"), Conversation: q.Get("conversation")}
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return invalid("query parameter since must be an RFC 3339 time")
		}
		f.Since = &t
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return invalid("query parameter limit must be an integer")
		}
		if n < 1 || n > core.MaxFeedbackLimit {
			return invalid("limit must be between 1 and %d", core.MaxFeedbackLimit)
		}
		f.Limit = n
	}
	page, err := h.svc.ListFeedback(r.Context(), p, f)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page)
	return nil
}

func (h *handler) getFeedback(w http.ResponseWriter, r *http.Request, p core.Principal) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return invalid("feedback id must be a positive integer")
	}
	f, err := h.svc.GetFeedback(r.Context(), p, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, f)
	return nil
}
