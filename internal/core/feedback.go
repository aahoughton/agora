package core

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// MaxFeedbackBytes caps a feedback note.
const MaxFeedbackBytes = 4000

// Feedback is an agent's note on smoot itself, left in one conversation.
// An agent has at most one note per conversation; submitting again
// replaces it.
type Feedback struct {
	ID           int64  `json:"id"`
	Namespace    string `json:"namespace"`
	Conversation string `json:"conversation"`
	Agent        string `json:"agent"`
	// Account is the account that last wrote the note; empty under auth
	// mode none.
	Account string `json:"account,omitempty"`
	// SeenSeq is how far the agent had read the conversation when it last
	// wrote the note.
	SeenSeq   int64     `json:"seen_seq"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FeedbackParams describe a feedback note from a participant.
type FeedbackParams struct {
	Agent string
	Body  string
}

// FeedbackResult is the stored note, and whether it replaced an earlier
// one from the same agent in the same conversation.
type FeedbackResult struct {
	Feedback Feedback `json:"feedback"`
	Replaced bool     `json:"replaced"`
}

// FeedbackEnabled reports whether agents may leave feedback
// (Options.Feedback). The MCP server registers its feedback tool only then.
func (s *Service) FeedbackEnabled() bool { return s.opts.Feedback }

// SubmitFeedback stores agent's note on smoot in conversation name,
// replacing the agent's earlier note there. The agent must have joined the
// conversation (any state, concluded or not) under the caller's account.
// It fails unless the server enables feedback (Options.Feedback).
func (s *Service) SubmitFeedback(ctx context.Context, p Principal, ns, name string, fp FeedbackParams) (FeedbackResult, error) {
	if !s.opts.Feedback {
		return FeedbackResult{}, errorf(CodeForbidden, "feedback is off on this server")
	}
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return FeedbackResult{}, err
	}
	if err := s.limitWrite(p); err != nil {
		return FeedbackResult{}, err
	}
	if err := checkName("agent", fp.Agent); err != nil {
		return FeedbackResult{}, err
	}
	if strings.TrimSpace(fp.Body) == "" {
		return FeedbackResult{}, errorf(CodeInvalid, "body is empty")
	}
	if len(fp.Body) > MaxFeedbackBytes {
		return FeedbackResult{}, errorf(CodeMessageTooLarge, "body is %d bytes; the limit is %d", len(fp.Body), MaxFeedbackBytes)
	}
	var res FeedbackResult
	err := s.writeAs(ctx, p, ns, ActionParticipate, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		if err := s.checkOwner(ctx, tx, c, p, fp.Agent); err != nil {
			return err
		}
		pt, err := s.participant(ctx, tx, c.id, fp.Agent)
		if err != nil {
			return err
		}
		if pt == nil {
			return errorf(CodeNotParticipant, "%s never joined conversation %q", fp.Agent, c.Name)
		}
		var id int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM feedback WHERE conversation_id = ? AND agent = ?`,
			c.id, fp.Agent).Scan(&id)
		switch {
		case err == nil:
			res.Replaced = true
		case !isNoRows(err):
			return err
		}
		now := ms(s.now())
		account := sql.NullInt64{Int64: p.AccountID, Valid: !p.IsAnonymous()}
		if _, err := tx.ExecContext(ctx, `INSERT INTO feedback
			(conversation_id, agent, account_id, seen_seq, body, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (conversation_id, agent) DO UPDATE SET
				account_id = excluded.account_id, seen_seq = excluded.seen_seq,
				body = excluded.body, updated_at = excluded.updated_at`,
			c.id, fp.Agent, account, pt.ReadSeq, fp.Body, now, now); err != nil {
			return err
		}
		res.Feedback, err = s.feedbackWhere(ctx, tx, `f.conversation_id = ? AND f.agent = ?`, c.id, fp.Agent)
		return err
	})
	return res, err
}

// FeedbackFilter narrows ListFeedback. Zero fields do not filter.
type FeedbackFilter struct {
	Namespace string
	// Conversation requires Namespace.
	Conversation string
	// Since keeps notes written (or last replaced) at or after it.
	Since *time.Time
	// Limit caps the notes returned; 0 selects DefaultFeedbackLimit.
	Limit int
}

// Feedback listing limits.
const (
	DefaultFeedbackLimit = 50
	MaxFeedbackLimit     = 500
)

// FeedbackPage is the newest notes matching a filter. More is true when
// older ones were left out by the limit.
type FeedbackPage struct {
	Feedback []Feedback `json:"feedback"`
	More     bool       `json:"more"`
}

// ListFeedback returns feedback notes, most recently written first. Server
// admins only.
func (s *Service) ListFeedback(ctx context.Context, p Principal, f FeedbackFilter) (FeedbackPage, error) {
	if err := s.requireServerAdmin(ctx, p); err != nil {
		return FeedbackPage{}, err
	}
	switch {
	case f.Limit == 0:
		f.Limit = DefaultFeedbackLimit
	case f.Limit < 0 || f.Limit > MaxFeedbackLimit:
		return FeedbackPage{}, errorf(CodeInvalid, "limit must be between 1 and %d", MaxFeedbackLimit)
	}
	if f.Conversation != "" && f.Namespace == "" {
		return FeedbackPage{}, errorf(CodeInvalid, "a conversation filter needs a namespace")
	}
	var where []string
	var args []any
	if f.Namespace != "" {
		if _, err := namespaceID(ctx, s.db, f.Namespace); err != nil {
			return FeedbackPage{}, err
		}
		where, args = append(where, "n.name = ?"), append(args, f.Namespace)
	}
	if f.Conversation != "" {
		where, args = append(where, "c.name = ?"), append(args, f.Conversation)
	}
	if f.Since != nil {
		where, args = append(where, "f.updated_at >= ?"), append(args, ms(*f.Since))
	}
	cond := "1"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}
	fs, err := collect(ctx, s.db, func(r *sql.Rows) (Feedback, error) { return scanFeedback(r) }, feedbackQuery+` WHERE `+cond+
		` ORDER BY f.updated_at DESC, f.id DESC LIMIT ?`, append(args, f.Limit+1)...)
	if err != nil {
		return FeedbackPage{}, err
	}
	page := FeedbackPage{Feedback: fs}
	if page.Feedback == nil {
		page.Feedback = []Feedback{}
	}
	if len(fs) > f.Limit {
		page.Feedback, page.More = fs[:f.Limit], true
	}
	return page, nil
}

// GetFeedback returns one feedback note by id. Server admins only.
func (s *Service) GetFeedback(ctx context.Context, p Principal, id int64) (Feedback, error) {
	if err := s.requireServerAdmin(ctx, p); err != nil {
		return Feedback{}, err
	}
	return s.feedbackWhere(ctx, s.db, `f.id = ?`, id)
}

const feedbackQuery = `SELECT f.id, n.name, c.name, f.agent, coalesce(a.name, ''), f.seen_seq, f.body,
	f.created_at, f.updated_at
	FROM feedback f
	JOIN conversations c ON c.id = f.conversation_id
	JOIN namespaces n ON n.id = c.namespace_id
	LEFT JOIN accounts a ON a.id = f.account_id`

func (s *Service) feedbackWhere(ctx context.Context, q querier, cond string, args ...any) (Feedback, error) {
	f, err := scanFeedback(q.QueryRowContext(ctx, feedbackQuery+` WHERE `+cond, args...))
	if isNoRows(err) {
		return Feedback{}, errorf(CodeFeedbackNotFound, "no such feedback")
	}
	return f, err
}

func scanFeedback(row interface{ Scan(...any) error }) (Feedback, error) {
	var f Feedback
	var created, updated int64
	err := row.Scan(&f.ID, &f.Namespace, &f.Conversation, &f.Agent, &f.Account, &f.SeenSeq, &f.Body, &created, &updated)
	f.CreatedAt, f.UpdatedAt = fromMS(created), fromMS(updated)
	return f, err
}

// hasFeedback reports whether agent has left a note in conversation convID.
func hasFeedback(ctx context.Context, q querier, convID int64, agent string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM feedback WHERE conversation_id = ? AND agent = ?`,
		convID, agent).Scan(&n)
	return n > 0, err
}
