package core

import (
	"context"
	"database/sql"
	"strings"
)

func (s *Service) checkBody(body string, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(body) == "" {
		return errorf(CodeInvalid, "body is empty")
	}
	if len(body) > s.opts.MaxBodyBytes {
		return errorf(CodeMessageTooLarge, "body is %d bytes; the limit is %d", len(body), s.opts.MaxBodyBytes)
	}
	return nil
}

// SayParams describe a message from a participant.
type SayParams struct {
	Agent string
	Body  string
	// ReplyTo, if set, names the seq this message answers.
	ReplyTo *int64
	// ExpectedLastSeq, if set, rejects the post with CodeStaleContext when
	// another message has landed since.
	ExpectedLastSeq *int64
}

// SayResult is the appended message and the agent's standing after it.
type SayResult struct {
	Message  Message  `json:"message"`
	Standing Standing `json:"standing"`
}

// Say appends a message from a participant. A concluded participant who says
// something withdraws their conclusion and becomes active again.
func (s *Service) Say(ctx context.Context, p Principal, ns, name string, sp SayParams) (SayResult, error) {
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return SayResult{}, err
	}
	if err := checkName("agent", sp.Agent); err != nil {
		return SayResult{}, err
	}
	if err := s.checkBody(sp.Body, false); err != nil {
		return SayResult{}, err
	}
	var out SayResult
	if err := s.settle(ctx, ns, name); err != nil {
		return SayResult{}, err
	}
	err := s.write(ctx, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		if err := openOrError(c); err != nil {
			return err
		}
		pt, err := s.member(ctx, tx, c, sp.Agent)
		if err != nil {
			return err
		}
		if sp.ExpectedLastSeq != nil && *sp.ExpectedLastSeq != c.LastSeq {
			e := errorf(CodeStaleContext, "conversation %q has moved to seq %d since %d; read the new messages and decide whether to still post", name, c.LastSeq, *sp.ExpectedLastSeq)
			e.LastSeq = c.LastSeq
			return e
		}
		if sp.ReplyTo != nil && (*sp.ReplyTo < 1 || *sp.ReplyTo > c.LastSeq) {
			return errorf(CodeInvalid, "reply_to %d is not a message in conversation %q", *sp.ReplyTo, name)
		}
		if pt.State == StateConcluded {
			if _, err := s.appendMsg(ctx, tx, c, KindSystem, sp.Agent, sp.Agent+" withdrew their conclusion", nil); err != nil {
				return err
			}
		}
		m, err := s.appendMsg(ctx, tx, c, KindSay, sp.Agent, sp.Body, sp.ReplyTo)
		if err != nil {
			return err
		}
		if err := s.setState(ctx, tx, c, sp.Agent, StateActive, PresenceWorking); err != nil {
			return err
		}
		out.Message = m
		out.Standing, err = s.standing(ctx, tx, c, sp.Agent)
		return err
	})
	return out, err
}

// ObserveParams describe a comment from someone watching, not participating.
type ObserveParams struct {
	Name string
	Body string
}

// Observe appends an observer comment. Observers never join, hold no
// presence, and do not count toward any end condition.
func (s *Service) Observe(ctx context.Context, p Principal, ns, name string, op ObserveParams) (Message, error) {
	if err := s.authorize(ctx, p, ns, ActionObserve); err != nil {
		return Message{}, err
	}
	if err := checkName("observer", op.Name); err != nil {
		return Message{}, err
	}
	if err := s.checkBody(op.Body, false); err != nil {
		return Message{}, err
	}
	var out Message
	if err := s.settle(ctx, ns, name); err != nil {
		return Message{}, err
	}
	err := s.write(ctx, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		if err := openOrError(c); err != nil {
			return err
		}
		out, err = s.appendMsg(ctx, tx, c, KindObserver, op.Name, op.Body, nil)
		return err
	})
	return out, err
}

// Read limits.
const (
	DefaultReadLimit = 50
	MaxReadLimit     = 500
)

// ReadParams select messages to read.
type ReadParams struct {
	// Agent, if set, must be a participant. The read starts after their
	// read_seq unless After is set, and advances read_seq past what it
	// covers. Without After, the agent's own messages are skipped: it wrote
	// them. With After (re-reading, or checking whether a post landed), they
	// are included.
	Agent string
	After *int64
	Limit int
}

// ReadResult is a page of the log.
type ReadResult struct {
	Messages []Message `json:"messages"`
	LastSeq  int64     `json:"last_seq"`
	// More is true when messages past the last one returned exist.
	More     bool      `json:"more"`
	Standing *Standing `json:"standing,omitempty"`
}

// Read returns messages after a seq, oldest first.
func (s *Service) Read(ctx context.Context, p Principal, ns, name string, rp ReadParams) (ReadResult, error) {
	if err := s.authorize(ctx, p, ns, ActionRead); err != nil {
		return ReadResult{}, err
	}
	if rp.Agent != "" {
		if err := checkName("agent", rp.Agent); err != nil {
			return ReadResult{}, err
		}
	}
	switch {
	case rp.Limit == 0:
		rp.Limit = DefaultReadLimit
	case rp.Limit < 0 || rp.Limit > MaxReadLimit:
		return ReadResult{}, errorf(CodeInvalid, "limit must be between 1 and %d", MaxReadLimit)
	}
	if rp.After != nil && *rp.After < 0 {
		return ReadResult{}, errorf(CodeInvalid, "after must not be negative")
	}

	if rp.Agent == "" {
		c, err := s.loadConvRead(ctx, ns, name)
		if err != nil {
			return ReadResult{}, err
		}
		var after int64
		if rp.After != nil {
			after = *rp.After
		}
		out, _, err := s.page(ctx, s.db, c, after, rp.Limit, "")
		return out, err
	}

	var out ReadResult
	if err := s.settle(ctx, ns, name); err != nil {
		return ReadResult{}, err
	}
	err := s.write(ctx, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		pt, err := s.member(ctx, tx, c, rp.Agent)
		if err != nil {
			return err
		}
		after, skip := pt.ReadSeq, rp.Agent
		if rp.After != nil {
			if *rp.After > c.LastSeq {
				return errorf(CodeInvalid, "after %d is past the last message (#%d)", *rp.After, c.LastSeq)
			}
			after, skip = *rp.After, ""
		}
		var covered int64
		out, covered, err = s.page(ctx, tx, c, after, rp.Limit, skip)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE participants SET read_seq = max(read_seq, ?)
			WHERE conversation_id = ? AND agent = ?`, covered, c.id, rp.Agent); err != nil {
			return err
		}
		if err := s.touch(ctx, tx, c, rp.Agent, PresenceWorking); err != nil {
			return err
		}
		st, err := s.standing(ctx, tx, c, rp.Agent)
		out.Standing = &st
		return err
	})
	return out, err
}

// page returns up to limit messages in (after, c.LastSeq], skipping those
// by skipAuthor if it is set. covered is the highest seq the page accounts
// for: the last message returned when more follow, otherwise c.LastSeq.
func (s *Service) page(ctx context.Context, q querier, c *conv, after int64, limit int, skipAuthor string) (ReadResult, int64, error) {
	msgs, err := s.messages(ctx, q, c.id, after, c.LastSeq, limit+1, skipAuthor)
	if err != nil {
		return ReadResult{}, 0, err
	}
	out := ReadResult{Messages: msgs, LastSeq: c.LastSeq}
	covered := max(c.LastSeq, after)
	if len(msgs) > limit {
		out.Messages, out.More = msgs[:limit], true
		covered = msgs[limit-1].Seq
	}
	return out, covered, nil
}

// messages returns up to limit messages with after < seq <= upto, skipping
// those by skipAuthor if it is set. Callers pass the last_seq they loaded, so
// a page never mixes in later appends.
func (s *Service) messages(ctx context.Context, q querier, convID, after, upto int64, limit int, skipAuthor string) ([]Message, error) {
	return collect(ctx, q, func(rows *sql.Rows) (Message, error) {
		var m Message
		var created int64
		err := rows.Scan(&m.Seq, &m.Kind, &m.Author, &m.Body, &m.ReplyTo, &created)
		m.CreatedAt = fromMS(created)
		return m, err
	}, `SELECT seq, kind, author, body, reply_to, created_at
		FROM messages WHERE conversation_id = ? AND seq > ? AND seq <= ? AND author != ? ORDER BY seq LIMIT ?`,
		convID, after, upto, skipAuthor, limit)
}
