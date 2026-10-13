package core

import (
	"context"
	"database/sql"
	"strings"
	"time"
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
	// Tail, if set, reads only the last Tail messages and, for an agent,
	// marks everything before them read too: a late joiner skips the middle
	// on purpose. It cannot be combined with After.
	Tail int
	// MaxBytes, if set, ends the page early once message bodies total more
	// than this; the page always holds at least one message, and an
	// agent's read position moves only past what was returned.
	MaxBytes int
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
	switch {
	case rp.Tail < 0 || rp.Tail > MaxReadLimit:
		return ReadResult{}, errorf(CodeInvalid, "tail must be between 1 and %d", MaxReadLimit)
	case rp.Tail > 0 && rp.After != nil:
		return ReadResult{}, errorf(CodeInvalid, "use tail or after, not both")
	case rp.Tail > 0:
		rp.Limit = rp.Tail
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
		if rp.Tail > 0 {
			tailAfter, err := s.tailStart(ctx, s.db, c, rp.Tail, "")
			if err != nil {
				return ReadResult{}, err
			}
			after = max(after, tailAfter)
		}
		out, _, err := s.page(ctx, s.db, c, after, rp.Limit, "")
		out, _ = capBytes(out, 0, rp.MaxBytes)
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
		if rp.Tail > 0 {
			tailAfter, err := s.tailStart(ctx, tx, c, rp.Tail, skip)
			if err != nil {
				return err
			}
			after = max(after, tailAfter)
		}
		var covered int64
		out, covered, err = s.page(ctx, tx, c, after, rp.Limit, skip)
		if err != nil {
			return err
		}
		out, covered = capBytes(out, covered, rp.MaxBytes)
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

// tailStart returns the seq to read after so that a page holds the last n
// messages not by skipAuthor.
func (s *Service) tailStart(ctx context.Context, q querier, c *conv, n int, skipAuthor string) (int64, error) {
	var first sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT min(seq) FROM (SELECT seq FROM messages
		WHERE conversation_id = ? AND seq <= ? AND author != ? ORDER BY seq DESC LIMIT ?)`,
		c.id, c.LastSeq, skipAuthor, n).Scan(&first)
	if err != nil || !first.Valid {
		return 0, err
	}
	return first.Int64 - 1, nil
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

// capBytes ends a page before the message that takes its bodies past
// maxBytes, keeping at least one; covered moves back to the last message
// kept. maxBytes 0 keeps the page as it is.
func capBytes(out ReadResult, covered int64, maxBytes int) (ReadResult, int64) {
	if maxBytes <= 0 {
		return out, covered
	}
	total := 0
	for i, m := range out.Messages {
		total += len(m.Body)
		if total > maxBytes && i > 0 {
			out.Messages, out.More = out.Messages[:i], true
			return out, out.Messages[i-1].Seq
		}
	}
	return out, covered
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

// FollowLimit bounds the messages one Follow call returns.
const FollowLimit = 200

// Follow blocks until the conversation's log moves past after, or timeout
// passes, then returns the new messages (all authors). It is for viewers:
// it needs no participant, sets no presence, and moves no read position.
func (s *Service) Follow(ctx context.Context, p Principal, ns, name string, after int64, timeout time.Duration) (ReadResult, error) {
	if err := s.authorize(ctx, p, ns, ActionRead); err != nil {
		return ReadResult{}, err
	}
	if after < 0 || timeout < 0 {
		return ReadResult{}, errorf(CodeInvalid, "after and timeout must not be negative")
	}
	c, err := s.loadConvRead(ctx, ns, name)
	if err != nil {
		return ReadResult{}, err
	}
	if !s.hub.watch(c.id, s.opts.MaxWatchers) {
		return ReadResult{}, errorf(CodeTooManyWatchers, "conversation %q already has %d viewers; retry later", name, s.opts.MaxWatchers)
	}
	defer s.hub.unwatch(c.id)

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	timedOut := false
	for {
		ch := s.hub.channel(c.id)
		// loadConvRead records a due timebox expiry, which then shows up
		// as a new message.
		c, err = s.loadConvRead(ctx, ns, name)
		if err != nil {
			return ReadResult{}, err
		}
		if c.LastSeq > after {
			out, _, err := s.page(ctx, s.db, c, after, FollowLimit, "")
			return out, err
		}
		if timedOut {
			return ReadResult{Messages: []Message{}, LastSeq: c.LastSeq}, nil
		}
		// Wake at a pending timebox deadline so its expiry is reported.
		var expiry <-chan time.Time
		var expiryTimer *time.Timer
		if c.Status == StatusOpen && c.End == EndTimebox && c.EndsAt != nil {
			expiryTimer = time.NewTimer(max(c.EndsAt.Sub(s.now()), 0))
			expiry = expiryTimer.C
		}
		select {
		case <-ch:
		case <-expiry:
		case <-deadline.C:
			timedOut = true
		case <-ctx.Done():
		}
		if expiryTimer != nil {
			expiryTimer.Stop()
		}
		if ctx.Err() != nil {
			return ReadResult{}, ctx.Err()
		}
	}
}
