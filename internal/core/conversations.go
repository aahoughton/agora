package core

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EndCondition says how a conversation concludes.
type EndCondition string

// End conditions.
const (
	// EndNone: the conversation never concludes on its own. Nobody is
	// expected to stay; the creator may close it.
	EndNone EndCondition = "none"
	// EndAgreement: concludes when no participant is still active and at
	// least one has concluded.
	EndAgreement EndCondition = "agreement"
	// EndCoordinator: concludes when the creator closes it.
	EndCoordinator EndCondition = "coordinator"
	// EndTimebox: concludes when EndsAt passes.
	EndTimebox EndCondition = "timebox"
)

func (e EndCondition) valid() bool {
	switch e {
	case EndNone, EndAgreement, EndCoordinator, EndTimebox:
		return true
	}
	return false
}

// Status of a conversation.
type Status string

// Statuses.
const (
	StatusOpen      Status = "open"
	StatusConcluded Status = "concluded"
)

// Conversation is a bounded exchange between participants in a namespace.
type Conversation struct {
	Namespace    string        `json:"namespace"`
	Name         string        `json:"name"`
	Topic        string        `json:"topic"`
	End          EndCondition  `json:"end"`
	EndsAt       *time.Time    `json:"ends_at,omitempty"`
	Status       Status        `json:"status"`
	CreatedBy    string        `json:"created_by"`
	CreatedAt    time.Time     `json:"created_at"`
	ConcludedAt  *time.Time    `json:"concluded_at,omitempty"`
	LastSeq      int64         `json:"last_seq"`
	LastAt       time.Time     `json:"last_at"`
	Archived     bool          `json:"archived"`
	Participants []Participant `json:"participants"`
}

// Message kinds.
const (
	KindSay      = "say"
	KindConclude = "conclude"
	KindObserver = "observer"
	KindSystem   = "system"
)

// SystemAuthor is the author of system messages not about one agent.
const SystemAuthor = "agora"

// Message is one entry in a conversation's append-only log. System messages
// about one agent (joined, left, ...) carry that agent as Author.
type Message struct {
	Seq       int64     `json:"seq"`
	Kind      string    `json:"kind"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	ReplyTo   *int64    `json:"reply_to,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// conv is a conversation loaded with its row ids.
type conv struct {
	id   int64
	nsID int64
	// creatorAccount is the account that started the conversation; NULL
	// under auth mode none.
	creatorAccount sql.NullInt64
	Conversation
}

const convColumns = `c.id, c.namespace_id, n.name, c.name, c.topic, c.end_condition, c.ends_at,
	c.status, c.created_by, c.created_at, c.concluded_at, c.last_seq, c.last_at, c.created_by_account`

func scanConv(row interface{ Scan(...any) error }) (*conv, error) {
	var c conv
	var endsAt, concludedAt sql.NullInt64
	var created, lastAt int64
	err := row.Scan(&c.id, &c.nsID, &c.Namespace, &c.Name, &c.Topic, &c.End, &endsAt,
		&c.Status, &c.CreatedBy, &created, &concludedAt, &c.LastSeq, &lastAt, &c.creatorAccount)
	if err != nil {
		return nil, err
	}
	c.EndsAt = fromNullMS(endsAt)
	c.ConcludedAt = fromNullMS(concludedAt)
	c.CreatedAt = fromMS(created)
	c.LastAt = fromMS(lastAt)
	c.Participants = []Participant{}
	return &c, nil
}

func (s *Service) loadConv(ctx context.Context, q querier, ns, name string) (*conv, error) {
	nsID, err := namespaceID(ctx, q, ns)
	if err != nil {
		return nil, err
	}
	if err := checkName("conversation", name); err != nil {
		return nil, err
	}
	row := q.QueryRowContext(ctx, `SELECT `+convColumns+`
		FROM conversations c JOIN namespaces n ON n.id = c.namespace_id
		WHERE c.namespace_id = ? AND c.name = ?`, nsID, name)
	c, err := scanConv(row)
	if isNoRows(err) {
		return nil, errorf(CodeConversationNotFound, "conversation %q not found in namespace %q", name, ns)
	}
	if err != nil {
		return nil, err
	}
	c.Archived = s.now().Sub(c.LastAt) > s.opts.ArchiveAfter
	return c, nil
}

// loadConvTx loads a conversation inside a write transaction. A timebox that
// is due is reported as concluded but not recorded here: callers run settle
// before opening the transaction, so the expiry is committed on its own and
// survives a rejected write.
func (s *Service) loadConvTx(ctx context.Context, tx *wtx, ns, name string) (*conv, error) {
	c, err := s.loadConv(ctx, tx, ns, name)
	if err != nil {
		return nil, err
	}
	if s.timeboxDue(c) {
		c.Status = StatusConcluded
	}
	return c, nil
}

// settle records a due timebox expiry in its own transaction. Every write
// path calls it before its own transaction.
func (s *Service) settle(ctx context.Context, ns, name string) error {
	_, err := s.loadConvRead(ctx, ns, name)
	return err
}

// loadConvRead loads a conversation for a read path. If a timebox is due it
// records the expiry first, so readers never see an overdue open timebox.
func (s *Service) loadConvRead(ctx context.Context, ns, name string) (*conv, error) {
	c, err := s.loadConv(ctx, s.db, ns, name)
	if err != nil {
		return nil, err
	}
	if !s.timeboxDue(c) {
		return c, nil
	}
	err = s.write(ctx, func(tx *wtx) error {
		c, err = s.loadConv(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		if s.timeboxDue(c) {
			return s.concludeConv(ctx, tx, c, "timebox elapsed")
		}
		return nil
	})
	return c, err
}

func (s *Service) timeboxDue(c *conv) bool {
	return c.Status == StatusOpen && c.End == EndTimebox && c.EndsAt != nil && !s.now().Before(*c.EndsAt)
}

func (s *Service) concludeConv(ctx context.Context, tx *wtx, c *conv, reason string) error {
	if _, err := s.appendMsg(ctx, tx, c, KindSystem, SystemAuthor, "conversation concluded: "+reason, nil); err != nil {
		return err
	}
	now := s.now()
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET status = ?, concluded_at = ? WHERE id = ?`,
		StatusConcluded, ms(now), c.id); err != nil {
		return err
	}
	c.Status = StatusConcluded
	t := fromMS(ms(now))
	c.ConcludedAt = &t
	return nil
}

// appendMsg assigns the next seq and appends to the log. The caller holds the
// write transaction, so c.LastSeq is current.
func (s *Service) appendMsg(ctx context.Context, tx *wtx, c *conv, kind, author, body string, replyTo *int64) (Message, error) {
	now := s.now()
	m := Message{Seq: c.LastSeq + 1, Kind: kind, Author: author, Body: body, ReplyTo: replyTo, CreatedAt: fromMS(ms(now))}
	var rt sql.NullInt64
	if replyTo != nil {
		rt = sql.NullInt64{Int64: *replyTo, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages (conversation_id, seq, kind, author, body, reply_to, created_at, account_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.id, m.Seq, kind, author, body, rt, ms(now), tx.account); err != nil {
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET last_seq = ?, last_at = ? WHERE id = ?`,
		m.Seq, ms(now), c.id); err != nil {
		return Message{}, err
	}
	c.LastSeq = m.Seq
	c.LastAt = m.CreatedAt
	tx.touched(c.id)
	return m, nil
}

// maxTopicBytes bounds a conversation's one-line topic.
const maxTopicBytes = 500

// StartParams describe a new conversation.
type StartParams struct {
	Name  string
	Topic string
	End   EndCondition
	// EndsAt is required for EndTimebox and rejected otherwise.
	EndsAt *time.Time
	// Agent is the creator. The creator may close the conversation.
	Agent string
	// Join adds the creator as a participant.
	Join bool
}

// Start creates a conversation.
func (s *Service) Start(ctx context.Context, p Principal, ns string, sp StartParams) (Conversation, error) {
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return Conversation{}, err
	}
	if err := s.limitWrite(p); err != nil {
		return Conversation{}, err
	}
	if err := checkName("conversation", sp.Name); err != nil {
		return Conversation{}, err
	}
	if err := checkName("agent", sp.Agent); err != nil {
		return Conversation{}, err
	}
	if strings.ContainsAny(sp.Topic, "\r\n") || len(sp.Topic) > maxTopicBytes {
		return Conversation{}, errorf(CodeInvalid, "topic must be one line of at most %d bytes", maxTopicBytes)
	}
	if sp.End == "" {
		sp.End = EndNone
	}
	if !sp.End.valid() {
		return Conversation{}, errorf(CodeInvalid, "invalid end condition %q: want none, agreement, coordinator or timebox", sp.End)
	}
	now := s.now()
	switch {
	case sp.End == EndTimebox && sp.EndsAt == nil:
		return Conversation{}, errorf(CodeInvalid, "end condition timebox needs ends_at")
	case sp.End == EndTimebox && !sp.EndsAt.After(now):
		return Conversation{}, errorf(CodeInvalid, "ends_at must be in the future")
	case sp.End != EndTimebox && sp.EndsAt != nil:
		return Conversation{}, errorf(CodeInvalid, "ends_at is only valid with end condition timebox")
	}

	var out Conversation
	err := s.writeAs(ctx, p, ns, ActionParticipate, func(tx *wtx) error {
		nsID, err := namespaceID(ctx, tx, ns)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO conversations
			(namespace_id, name, topic, end_condition, ends_at, created_by, created_at, last_at, created_by_account)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nsID, sp.Name, sp.Topic, sp.End, nullMS(sp.EndsAt), sp.Agent, ms(now), ms(now), tx.account)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return errorf(CodeConversationExists, "conversation %q already exists in namespace %q; join it or pick another name", sp.Name, ns)
			}
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		c, err := s.loadConv(ctx, tx, ns, sp.Name)
		if err != nil {
			return err
		}
		if c.id != id {
			return fmt.Errorf("core: inserted conversation id %d, loaded %d", id, c.id)
		}
		started := fmt.Sprintf("%s started the conversation (end: %s)", sp.Agent, sp.End)
		if _, err := s.appendMsg(ctx, tx, c, KindSystem, sp.Agent, started, nil); err != nil {
			return err
		}
		if sp.Join {
			if err := s.addParticipant(ctx, tx, c, sp.Agent); err != nil {
				return err
			}
		}
		out, err = s.conversationWithParticipants(ctx, tx, c)
		return err
	})
	return out, err
}

// Listing bounds.
const (
	DefaultListLimit = 100
	MaxListLimit     = 500
)

// ListParams filter and page ListConversations.
type ListParams struct {
	// Status keeps only open or only concluded conversations; empty keeps
	// both. A timebox past its end counts as concluded.
	Status Status
	// IncludeArchived keeps conversations idle longer than ArchiveAfter.
	IncludeArchived bool
	// Limit caps the page; 0 means DefaultListLimit.
	Limit int
	// Before continues a listing from an earlier page's Next.
	Before string
}

// ListPage is one page of conversations, most recently active first.
type ListPage struct {
	Conversations []Conversation `json:"conversations"`
	// Next continues the listing as ListParams.Before; empty on the last
	// page.
	Next string `json:"next,omitempty"`
}

// listCursor is a position in a listing: the last_at and id of the last
// conversation on the page before.
type listCursor struct{ at, id int64 }

func (c listCursor) String() string { return fmt.Sprintf("%d.%d", c.at, c.id) }

func parseListCursor(s string) (listCursor, error) {
	at, id, ok := strings.Cut(s, ".")
	var c listCursor
	var err1, err2 error
	c.at, err1 = strconv.ParseInt(at, 10, 64)
	c.id, err2 = strconv.ParseInt(id, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return listCursor{}, errorf(CodeInvalid, "before %q is not a cursor from an earlier page", s)
	}
	return c, nil
}

// ListConversations returns a page of a namespace's conversations, most
// recently active first.
func (s *Service) ListConversations(ctx context.Context, p Principal, ns string, lp ListParams) (ListPage, error) {
	if err := s.authorize(ctx, p, ns, ActionRead); err != nil {
		return ListPage{}, err
	}
	limit := lp.Limit
	switch {
	case limit == 0:
		limit = DefaultListLimit
	case limit < 0 || limit > MaxListLimit:
		return ListPage{}, errorf(CodeInvalid, "limit must be between 1 and %d", MaxListLimit)
	}
	nsID, err := namespaceID(ctx, s.db, ns)
	if err != nil {
		return ListPage{}, err
	}
	now := ms(s.now())
	where := []string{"c.namespace_id = ?"}
	args := []any{nsID}
	// A timebox whose end has passed is concluded even before its expiry
	// is recorded.
	const due = "(c.end_condition = 'timebox' AND c.ends_at <= ?)"
	switch lp.Status {
	case "":
	case StatusOpen:
		where = append(where, "c.status = 'open' AND NOT "+due)
		args = append(args, now)
	case StatusConcluded:
		where = append(where, "(c.status = 'concluded' OR "+due+")")
		args = append(args, now)
	default:
		return ListPage{}, errorf(CodeInvalid, "status must be open or concluded, or empty for both")
	}
	if !lp.IncludeArchived {
		where = append(where, "c.last_at >= ?")
		args = append(args, now-s.opts.ArchiveAfter.Milliseconds())
	}
	if lp.Before != "" {
		cur, err := parseListCursor(lp.Before)
		if err != nil {
			return ListPage{}, err
		}
		where = append(where, "(c.last_at < ? OR (c.last_at = ? AND c.id < ?))")
		args = append(args, cur.at, cur.at, cur.id)
	}
	args = append(args, limit+1)
	rows, err := collect(ctx, s.db, func(rows *sql.Rows) (*conv, error) { return scanConv(rows) },
		`SELECT `+convColumns+` FROM conversations c JOIN namespaces n ON n.id = c.namespace_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY c.last_at DESC, c.id DESC LIMIT ?`, args...)
	if err != nil {
		return ListPage{}, err
	}
	page := ListPage{Conversations: []Conversation{}}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.Next = listCursor{ms(last.LastAt), last.id}.String()
	}
	ids := make([]int64, len(rows))
	for i, c := range rows {
		ids[i] = c.id
	}
	pts, err := s.participantsOf(ctx, s.db, ids)
	if err != nil {
		return ListPage{}, err
	}
	for _, c := range rows {
		if s.timeboxDue(c) {
			fresh, err := s.loadConvRead(ctx, ns, c.Name)
			if err != nil {
				return ListPage{}, err
			}
			c = fresh
		}
		c.Archived = s.now().Sub(c.LastAt) > s.opts.ArchiveAfter
		out := c.Conversation
		out.Participants = pts[c.id]
		if out.Participants == nil {
			out.Participants = []Participant{}
		}
		page.Conversations = append(page.Conversations, out)
	}
	return page, nil
}

// GetConversation returns one conversation with its participants.
func (s *Service) GetConversation(ctx context.Context, p Principal, ns, name string) (Conversation, error) {
	if err := s.authorize(ctx, p, ns, ActionRead); err != nil {
		return Conversation{}, err
	}
	c, err := s.loadConvRead(ctx, ns, name)
	if err != nil {
		return Conversation{}, err
	}
	return s.conversationWithParticipants(ctx, s.db, c)
}

func (s *Service) conversationWithParticipants(ctx context.Context, q querier, c *conv) (Conversation, error) {
	ps, err := s.participants(ctx, q, c.id)
	if err != nil {
		return Conversation{}, err
	}
	out := c.Conversation
	out.Participants = ps
	return out, nil
}
