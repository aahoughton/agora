package tui

import (
	"context"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aahoughton/smoot/internal/core"
)

// searchPage is how many hits the search screen loads at a time.
const searchPage = 50

// focusContext is how many messages before a search hit load with it when
// the hit is older than the messages a conversation shows by default.
const focusContext = 3

// search is the state of the search screen. It outlives a conversation
// opened from it, so esc there comes back to the same results.
type search struct {
	input   textinput.Model
	editing bool
	query   string
	order   string
	// loaded is false until the first page arrives.
	loaded      bool
	hits        []core.SearchHit
	total       int
	next        string
	loadingMore bool
	cursor      int
}

type searchMsg struct {
	gen  int
	page core.SearchPage
	more bool
	err  error
}

// searchOrders is the order key's cycle.
var searchOrders = []string{core.OrderRelevance, core.OrderNewest, core.OrderOldest}

// openSearch opens the search screen with an empty query.
func (m *Model) openSearch() tea.Cmd {
	m.leave()
	m.screen = screenSearch
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = core.MaxQueryBytes
	m.search = &search{input: in, editing: true, order: core.OrderRelevance}
	return m.search.input.Focus()
}

// returnToSearch shows the search screen as it was left. A page that was
// loading when it was left was cancelled, so paging starts again from the
// last page that arrived.
func (m *Model) returnToSearch() tea.Cmd {
	s := m.search
	m.leave()
	m.screen, m.search = screenSearch, s
	s.loadingMore = false
	return nil
}

// runSearch loads the first page for the current query and order.
func (m *Model) runSearch() tea.Cmd {
	s := m.search
	m.gen++
	s.loaded, s.hits, s.total, s.next, s.loadingMore, s.cursor = false, nil, 0, "", false, 0
	return m.searchRequest(core.SearchParams{Query: s.query, Order: s.order, Limit: searchPage}, false)
}

func (m *Model) searchMore() tea.Cmd {
	s := m.search
	if s.next == "" || s.loadingMore {
		return nil
	}
	s.loadingMore = true
	return m.searchRequest(core.SearchParams{Query: s.query, Order: s.order, Limit: searchPage, Cursor: s.next}, true)
}

func (m *Model) searchRequest(sp core.SearchParams, more bool) tea.Cmd {
	gen, c, ctx, ns := m.gen, m.opts.Client, m.screenCtx, m.ns
	return func() tea.Msg {
		ctx, done := context.WithTimeout(ctx, requestTimeout)
		defer done()
		page, err := c.Search(ctx, ns, sp)
		return searchMsg{gen: gen, page: page, more: more, err: err}
	}
}

func (m *Model) searched(msg searchMsg) {
	s := m.search
	m.setNotice(msg.err)
	if msg.more {
		s.loadingMore = false
	}
	if msg.err != nil {
		return
	}
	s.loaded, s.total, s.next = true, msg.page.Total, msg.page.Next
	s.hits = append(s.hits, msg.page.Hits...)
}

func (m *Model) searchKey(k tea.KeyPressMsg) tea.Cmd {
	s := m.search
	if s.editing {
		switch k.String() {
		case "esc":
			if !s.loaded && s.query == "" {
				return m.openConversations(m.ns)
			}
			s.editing = false
			s.input.SetValue(s.query)
			s.input.Blur()
			return nil
		case "enter":
			q := strings.TrimSpace(s.input.Value())
			if q == "" {
				return nil
			}
			s.query, s.editing = q, false
			s.input.Blur()
			return m.runSearch()
		}
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(k)
		return cmd
	}
	switch k.String() {
	case "up", "k":
		s.cursor = max(s.cursor-1, 0)
	case "down", "j":
		s.cursor = min(s.cursor+1, max(len(s.hits)-1, 0))
	case "home", "g":
		s.cursor = 0
	case "end", "G":
		s.cursor = max(len(s.hits)-1, 0)
	case "s", "/":
		s.editing = true
		return s.input.Focus()
	case "o":
		for i, o := range searchOrders {
			if o == s.order {
				s.order = searchOrders[(i+1)%len(searchOrders)]
				break
			}
		}
		return m.runSearch()
	case "esc":
		ns := m.ns
		m.search = nil
		return m.openConversations(ns)
	case "enter":
		if s.cursor < len(s.hits) {
			h := s.hits[s.cursor]
			return m.openConversationAt(h.Conversation, h.Seq)
		}
	}
	// Reaching the end of what is loaded fetches the next page.
	if s.cursor >= len(s.hits)-1 {
		return m.searchMore()
	}
	return nil
}

// openConversationAt opens a conversation with message seq at the top of
// the view.
func (m *Model) openConversationAt(name string, seq int64) tea.Cmd {
	s := m.search
	cmd := m.openConversation(name)
	m.search = s
	m.t.focus = seq
	return cmd
}

// placeFocus scrolls the thread so the focus message is at the top, once
// it has loaded. It reports whether it did.
func (m *Model) placeFocus() bool {
	t := m.t
	if t.focus == 0 {
		return false
	}
	off := 0
	if t.truncated {
		off = 2
	}
	for i, mm := range t.messages {
		if mm.Seq == t.focus {
			// Skip the blank line that separates it from the message
			// before.
			if len(t.lines[i]) > 0 && t.lines[i][0] == "" {
				off++
			}
			t.vp.SetYOffset(off)
			t.focus = 0
			return true
		}
		off += len(t.lines[i])
	}
	return false
}

func (m *Model) searchView() []string {
	s := m.search
	r := m.render()
	head := m.st.accent.Bold(true).Render(m.say("brand", nil)) + "  " +
		m.st.fg.Bold(true).Render(m.say("search_title", map[string]any{"Namespace": m.ns})) + "  "
	if s.editing {
		// The query box takes the rest of the line.
		s.input.SetWidth(max(m.width-ansi.StringWidth(head)-1, 10))
		head += s.input.View()
	} else {
		head += s.query + "  " + m.st.faint.Render(m.say("search_order", map[string]any{"Order": s.order}))
	}
	out := []string{ansi.Truncate(head, m.width, m.st.g.more), r.rule(m.width)}
	hints := m.say("hints_search", nil)
	if s.editing {
		hints = m.say("hints_search_input", nil)
	}
	bodyH := max(m.height-3, 1)
	if m.help {
		return append(append(out, fit(m.helpLines(), m.width, bodyH)...), m.footer(hints))
	}

	var rows []string
	switch {
	case s.query == "":
		rows = []string{"  " + m.st.muted.Render(m.say("search_prompt", nil))}
	case !s.loaded && m.notice == "":
		rows = []string{"  " + m.st.faint.Render(m.say("loading", nil))}
	case s.loaded && s.total == 0:
		rows = []string{"  " + m.st.muted.Render(m.say("search_empty", nil))}
	case s.loaded:
		rows = append(rows, "  "+m.st.faint.Render(m.say("search_count", map[string]any{"Total": s.total})), "")
	}
	// Each hit takes two lines; keep the cursor's on screen.
	first := len(rows)
	for i, h := range s.hits {
		mark := "  "
		if i == s.cursor {
			mark = m.st.accent.Render(m.st.g.cursor + " ")
		}
		rows = append(rows, mark+r.searchHit(h, m.width-2), "    "+r.snippet(h, m.width-4))
	}
	start := 0
	if end := first + 2*s.cursor + 2; end > bodyH {
		start = end - bodyH
	}
	out = append(out, fit(rows[min(start, len(rows)):], m.width, bodyH)...)
	return append(out, m.footer(hints))
}

// searchHit is a hit's first line: where it is, who wrote it and when.
func (r renderer) searchHit(h core.SearchHit, width int) string {
	line := r.st.fg.Bold(true).Render(h.Conversation) + " " + r.st.faint.Render("#") + r.st.muted.Render(strconv.FormatInt(h.Seq, 10)) +
		"  " + r.st.toneOf(h.Author).Render(sanitize(h.Author))
	switch h.Kind {
	case core.KindObserver:
		line += " " + r.st.observer.Render(r.sk.say("kind_observer", nil))
	case core.KindConclude:
		line += " " + r.st.done.Render(r.sk.say("kind_conclude", nil))
	}
	meta := r.datetime(h.CreatedAt)
	metaW := ansi.StringWidth(meta)
	if width-metaW-2 < 20 {
		return ansi.Truncate(line, width, r.st.g.more)
	}
	line = ansi.Truncate(line, width-metaW-2, r.st.g.more)
	return line + strings.Repeat(" ", width-metaW-ansi.StringWidth(line)) + r.st.faint.Render(meta)
}

// snippet is a hit's second line, with the matched words marked. Its text
// is sanitized like a body, so it cannot drive the terminal.
func (r renderer) snippet(h core.SearchHit, width int) string {
	var b strings.Builder
	for _, p := range h.Snippet {
		text := sanitize(p.Text)
		if p.Match {
			b.WriteString(r.st.accent.Bold(true).Render(text))
		} else {
			b.WriteString(r.st.muted.Render(text))
		}
	}
	return ansi.Truncate(b.String(), width, r.st.g.more)
}
