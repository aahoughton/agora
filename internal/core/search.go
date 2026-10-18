package core

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Search limits.
const (
	DefaultSearchLimit = 25
	MaxSearchLimit     = 100
	// MaxQueryBytes bounds a search query.
	MaxQueryBytes = 500
	// maxQueryTerms bounds the words and phrases in one query.
	maxQueryTerms = 20
	// snippetTokens is how many words of context a snippet shows.
	snippetTokens = 16
)

// Search orders.
const (
	OrderRelevance = "relevance"
	OrderNewest    = "newest"
	OrderOldest    = "oldest"
)

// SearchParams describe a search of one namespace's messages.
type SearchParams struct {
	// Query is words, "quoted phrases" and prefix* terms, all of which a
	// message must contain.
	Query string
	// Conversation and Author, if set, narrow the search to one
	// conversation or one author.
	Conversation string
	Author       string
	// Since and Until, if set, bound when the message was posted
	// (inclusive).
	Since, Until *time.Time
	// Order is OrderRelevance (the default), OrderNewest or OrderOldest.
	Order string
	// Limit is the page size: DefaultSearchLimit when 0, at most
	// MaxSearchLimit.
	Limit int
	// Cursor continues an earlier search from its SearchPage.Next. It
	// belongs to the order it came from.
	Cursor string
}

// SearchPage is one page of search results.
type SearchPage struct {
	// Total counts every message that matches, on all pages.
	Total int         `json:"total"`
	Hits  []SearchHit `json:"hits"`
	// Next continues the search as SearchParams.Cursor; empty on the last
	// page.
	Next string `json:"next,omitempty"`
}

// SearchHit is one matching message.
type SearchHit struct {
	Conversation string    `json:"conversation"`
	Seq          int64     `json:"seq"`
	Kind         string    `json:"kind"`
	Author       string    `json:"author"`
	CreatedAt    time.Time `json:"created_at"`
	// YourAccount is true when the caller's account posted the message.
	YourAccount bool `json:"your_account"`
	// Snippet is the part of the body around the match, on one line, in
	// segments; Match marks the words the query matched.
	Snippet []SnippetPart `json:"snippet"`
}

// SnippetPart is a run of a snippet's text.
type SnippetPart struct {
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

// Text is the snippet as plain text.
func (h SearchHit) Text() string {
	var b strings.Builder
	for _, p := range h.Snippet {
		b.WriteString(p.Text)
	}
	return b.String()
}

// Snippet markers: private-use runes, which a body that contains them can
// confuse but which no text means to use.
const (
	markOpen  = ""
	markClose = ""
)

// Search returns a page of the messages in namespace ns that match the
// query. System lines are not searched.
func (s *Service) Search(ctx context.Context, p Principal, ns string, sp SearchParams) (SearchPage, error) {
	if err := s.authorize(ctx, p, ns, ActionRead); err != nil {
		return SearchPage{}, err
	}
	match, err := ftsQuery(sp.Query)
	if err != nil {
		return SearchPage{}, err
	}
	limit := sp.Limit
	switch {
	case limit == 0:
		limit = DefaultSearchLimit
	case limit < 0 || limit > MaxSearchLimit:
		return SearchPage{}, errorf(CodeInvalid, "limit must be between 1 and %d", MaxSearchLimit)
	}
	order := sp.Order
	if order == "" {
		order = OrderRelevance
	}
	if order != OrderRelevance && order != OrderNewest && order != OrderOldest {
		return SearchPage{}, errorf(CodeInvalid, "order must be relevance, newest or oldest")
	}
	cur, err := parseSearchCursor(sp.Cursor, order)
	if err != nil {
		return SearchPage{}, err
	}
	nsID, err := namespaceID(ctx, s.db, ns)
	if err != nil {
		return SearchPage{}, err
	}

	where := []string{"messages_fts MATCH ?", "c.namespace_id = ?"}
	args := []any{match, nsID}
	if sp.Conversation != "" {
		where = append(where, "c.name = ?")
		args = append(args, sp.Conversation)
	}
	if sp.Author != "" {
		where = append(where, "m.author = ?")
		args = append(args, sp.Author)
	}
	if sp.Since != nil {
		where = append(where, "m.created_at >= ?")
		args = append(args, ms(*sp.Since))
	}
	if sp.Until != nil {
		where = append(where, "m.created_at <= ?")
		args = append(args, ms(*sp.Until))
	}
	const from = ` FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid
		JOIN conversations c ON c.id = m.conversation_id WHERE `

	page := SearchPage{Hits: []SearchHit{}}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*)`+from+strings.Join(where, " AND "), args...).Scan(&page.Total); err != nil {
		return SearchPage{}, err
	}

	var orderBy string
	switch order {
	case OrderRelevance:
		orderBy = "bm25(messages_fts), m.created_at DESC, m.id DESC"
	case OrderNewest:
		orderBy = "m.created_at DESC, m.id DESC"
		if cur.set {
			where = append(where, "(m.created_at < ? OR (m.created_at = ? AND m.id < ?))")
			args = append(args, cur.at, cur.at, cur.id)
		}
	case OrderOldest:
		orderBy = "m.created_at, m.id"
		if cur.set {
			where = append(where, "(m.created_at > ? OR (m.created_at = ? AND m.id > ?))")
			args = append(args, cur.at, cur.at, cur.id)
		}
	}
	args = append(args, limit+1, cur.offset)
	rows, err := collect(ctx, s.db, scanHit,
		`SELECT c.name, m.seq, m.kind, m.author, m.created_at, m.id, m.account_id,
			snippet(messages_fts, 0, '`+markOpen+`', '`+markClose+`', '...', `+strconv.Itoa(snippetTokens)+`)`+
			from+strings.Join(where, " AND ")+` ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return SearchPage{}, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next := searchCursor{set: true}
		if order == OrderRelevance {
			next.offset = cur.offset + limit
		} else {
			next.at, next.id = ms(last.CreatedAt), last.id
		}
		page.Next = next.format(order)
	}
	for _, h := range rows {
		h.YourAccount = p.owns(h.account)
		page.Hits = append(page.Hits, h.SearchHit)
	}
	return page, nil
}

type hit struct {
	SearchHit
	id      int64
	account sql.NullInt64
}

func scanHit(rows *sql.Rows) (hit, error) {
	var h hit
	var at int64
	var snippet string
	if err := rows.Scan(&h.Conversation, &h.Seq, &h.Kind, &h.Author, &at, &h.id, &h.account, &snippet); err != nil {
		return hit{}, err
	}
	h.CreatedAt = time.UnixMilli(at).UTC()
	h.Snippet = snippetParts(snippet)
	return h, nil
}

// snippetParts splits a marked snippet into parts and folds its whitespace
// to single spaces, so it reads as one line.
func snippetParts(s string) []SnippetPart {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	var parts []SnippetPart
	for s != "" {
		before, rest, found := strings.Cut(s, markOpen)
		if before != "" {
			parts = append(parts, SnippetPart{Text: before})
		}
		if !found {
			break
		}
		matched, after, _ := strings.Cut(rest, markClose)
		if matched != "" {
			parts = append(parts, SnippetPart{Text: matched, Match: true})
		}
		s = after
	}
	if parts == nil {
		parts = []SnippetPart{}
	}
	return parts
}

// ftsQuery turns a search query into an FTS5 expression. Words and quoted
// phrases are each quoted, so no input is an FTS5 syntax error; a trailing
// * makes a word a prefix. FTS5 ANDs the terms.
func ftsQuery(q string) (string, error) {
	if len(q) > MaxQueryBytes {
		return "", errorf(CodeInvalid, "query is longer than %d bytes", MaxQueryBytes)
	}
	// SQLite ends the expression at a NUL, which would leave a phrase
	// unterminated.
	if strings.ContainsRune(q, 0) {
		return "", errorf(CodeInvalid, "query contains a NUL character")
	}
	var terms []string
	add := func(t string, prefix bool) {
		// A term the tokenizer would drop (punctuation only) matches
		// nothing, so leave it out.
		if !strings.ContainsFunc(t, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
			return
		}
		quoted := `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
		if prefix {
			quoted += "*"
		}
		terms = append(terms, quoted)
	}
	rest := q
	for {
		rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
		if rest == "" {
			break
		}
		if phrase, ok := strings.CutPrefix(rest, `"`); ok {
			text, after, _ := strings.Cut(phrase, `"`)
			add(text, false)
			rest = after
			continue
		}
		end := strings.IndexFunc(rest, unicode.IsSpace)
		if end < 0 {
			end = len(rest)
		}
		word := rest[:end]
		rest = rest[end:]
		word, prefix := strings.CutSuffix(word, "*")
		add(strings.ReplaceAll(word, `"`, ""), prefix)
	}
	if len(terms) == 0 {
		return "", errorf(CodeInvalid, "query has no words to search for")
	}
	if len(terms) > maxQueryTerms {
		return "", errorf(CodeInvalid, "query has more than %d words and phrases", maxQueryTerms)
	}
	return strings.Join(terms, " "), nil
}

// searchCursor is a position in search results: an offset for relevance
// order, or the created_at and id of the last hit for newest and oldest.
type searchCursor struct {
	set    bool
	offset int
	at, id int64
}

func (c searchCursor) format(order string) string {
	if order == OrderRelevance {
		return fmt.Sprintf("r%d", c.offset)
	}
	return fmt.Sprintf("%c%d.%d", order[0], c.at, c.id)
}

func parseSearchCursor(s, order string) (searchCursor, error) {
	if s == "" {
		return searchCursor{}, nil
	}
	bad := errorf(CodeInvalid, "cursor %q is not from an earlier page of a search in %s order", s, order)
	if s[0] != order[0] {
		return searchCursor{}, bad
	}
	c := searchCursor{set: true}
	if order == OrderRelevance {
		n, err := strconv.Atoi(s[1:])
		if err != nil || n < 0 {
			return searchCursor{}, bad
		}
		c.offset = n
		return c, nil
	}
	at, id, ok := strings.Cut(s[1:], ".")
	var err1, err2 error
	c.at, err1 = strconv.ParseInt(at, 10, 64)
	c.id, err2 = strconv.ParseInt(id, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return searchCursor{}, bad
	}
	return c, nil
}
