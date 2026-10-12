package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// You values in a Standing.
const (
	YouNotJoined = "not_joined"
	YouActive    = StateActive
	YouConcluded = StateConcluded
	YouLeft      = StateLeft
	YouDropped   = StateDropped
)

// Next values in a Standing.
const (
	NextRead = "read"
	NextWait = "wait"
	NextJoin = "join"
	NextDone = "done"
	// NextClose tells a coordinator that nobody else is active.
	NextClose = "close"
)

// Standing is one agent's position in one conversation. Every agent-facing
// result carries it so the agent always knows whether it is still expected.
type Standing struct {
	Namespace    string `json:"namespace"`
	Conversation string `json:"conversation"`
	Status       Status `json:"status"`
	You          string `json:"you"`
	// Expected is true while the conversation is waiting on this agent.
	Expected     bool   `json:"expected"`
	Unread       int    `json:"unread"`
	RepliesToYou int    `json:"replies_to_you"`
	Next         string `json:"next"`
	// Line is a one-line, human-readable summary of the fields above.
	Line string `json:"line"`
}

func (s *Service) standing(ctx context.Context, q querier, c *conv, agent string) (Standing, error) {
	st := Standing{Namespace: c.Namespace, Conversation: c.Name, Status: c.Status, You: YouNotJoined}
	pt, err := s.participant(ctx, q, c.id, agent)
	if err != nil {
		return Standing{}, err
	}
	if pt != nil {
		st.You = pt.State
		st.Expected = expected(c, pt)
		// Counts stop at c.LastSeq so they agree with the rest of the result
		// even if messages land while it is being built.
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM messages
			WHERE conversation_id = ? AND seq > ? AND seq <= ? AND author != ?`,
			c.id, pt.ReadSeq, c.LastSeq, agent).Scan(&st.Unread); err != nil {
			return Standing{}, err
		}
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM messages
			WHERE conversation_id = ? AND seq > ? AND seq <= ? AND author != ? AND reply_to IN
				(SELECT seq FROM messages WHERE conversation_id = ? AND author = ? AND kind IN ('say', 'conclude'))`,
			c.id, pt.ReadSeq, c.LastSeq, agent, c.id, agent).Scan(&st.RepliesToYou); err != nil {
			return Standing{}, err
		}
	}
	canClose, err := s.creatorCanClose(ctx, q, c, agent)
	if err != nil {
		return Standing{}, err
	}

	var advice string
	switch {
	case c.Status == StatusConcluded && st.Unread > 0:
		st.Next, advice = NextRead, "the conversation has concluded; read the remaining messages, then stop"
	case c.Status == StatusConcluded:
		st.Next, advice = NextDone, "the conversation has concluded; you are free to stop"
	case canClose:
		st.Next, advice = NextClose, "everyone else has concluded; read their conclusions, then close the conversation"
	case (st.You == YouLeft || st.You == YouNotJoined) && c.End == EndCoordinator && agent == c.CreatedBy:
		st.Next, advice = NextJoin, "you started this conversation and stepped away; rejoin to follow along, or check status later to learn when to close it"
	case st.You == YouNotJoined:
		st.Next, advice = NextJoin, "join to take part"
	case st.You == YouLeft || st.You == YouDropped:
		st.Next, advice = NextJoin, "you left; rejoin to take part"
	case st.Unread > 0:
		st.Next, advice = NextRead, "read the new messages, then reply or wait"
	case st.You == YouConcluded:
		st.Next, advice = NextWait, "you concluded; wait in case someone replies or withdraws"
	case st.Expected:
		st.Next, advice = NextWait, "wait for replies; do not end your turn while you are expected"
	default:
		st.Next, advice = NextWait, "wait for replies, or leave when you are done"
	}
	you := st.You
	if st.Expected {
		you += ", expected"
	}
	st.Line = fmt.Sprintf("%s: %s; you: %s; unread: %d (%d replies to you); next: %s (%s)",
		c.Name, c.Status, you, st.Unread, st.RepliesToYou, st.Next, advice)
	return st, nil
}

// creatorCanClose reports whether agent started this open coordinator
// conversation and nobody else is still active, with at least one other
// participant concluded.
func (s *Service) creatorCanClose(ctx context.Context, q querier, c *conv, agent string) (bool, error) {
	if c.Status != StatusOpen || c.End != EndCoordinator || agent != c.CreatedBy {
		return false, nil
	}
	var active, concluded int
	err := q.QueryRowContext(ctx, `SELECT
		coalesce(sum(state = 'active'), 0), coalesce(sum(state = 'concluded'), 0)
		FROM participants WHERE conversation_id = ? AND agent != ?`, c.id, agent).Scan(&active, &concluded)
	return active == 0 && concluded > 0, err
}

// Preview is a short form of one message in a Digest.
type Preview struct {
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"`
	Author  string `json:"author"`
	ReplyTo *int64 `json:"reply_to,omitempty"`
	Text    string `json:"text"`
}

// AuthorCount counts new messages from one author.
type AuthorCount struct {
	Author string `json:"author"`
	Count  int    `json:"count"`
}

// Digest summarizes new activity without message bodies. Bodies come from
// Read, which also advances the reader's position.
type Digest struct {
	After    int64         `json:"after"`
	LastSeq  int64         `json:"last_seq"`
	New      int           `json:"new"`
	Authors  []AuthorCount `json:"authors"`
	Previews []Preview     `json:"previews"`
	TimedOut bool          `json:"timed_out"`
	Standing Standing      `json:"standing"`
}

// Digest limits.
const (
	maxPreviews    = 5
	maxPreviewText = 120
)

// WaitParams describe a wait.
type WaitParams struct {
	Agent string
	// After defaults to the agent's read_seq.
	After   *int64
	Timeout time.Duration
}

// Wait blocks until another author appends past After, the conversation
// concludes, or Timeout passes, then returns a Digest. A timeout is a normal
// result with TimedOut set. While blocked the agent's presence is `waiting`.
func (s *Service) Wait(ctx context.Context, p Principal, ns, name string, wp WaitParams) (Digest, error) {
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return Digest{}, err
	}
	if err := checkName("agent", wp.Agent); err != nil {
		return Digest{}, err
	}
	if wp.Timeout < 0 {
		return Digest{}, errorf(CodeInvalid, "timeout must not be negative")
	}
	if wp.After != nil && *wp.After < 0 {
		return Digest{}, errorf(CodeInvalid, "after must not be negative")
	}

	if err := s.settle(ctx, ns, name); err != nil {
		return Digest{}, err
	}
	var c *conv
	var after int64
	err := s.write(ctx, func(tx *wtx) error {
		var err error
		c, err = s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		pt, err := s.member(ctx, tx, c, wp.Agent)
		if err != nil {
			return err
		}
		after = pt.ReadSeq
		if wp.After != nil {
			after = *wp.After
		}
		return s.touch(ctx, tx, c, wp.Agent, PresenceWaiting)
	})
	if err != nil {
		return Digest{}, err
	}

	if !s.hub.enter(c.id, s.opts.MaxWaiters) {
		return Digest{}, errorf(CodeTooManyWaiters, "conversation %q already has %d waiters; retry later", name, s.opts.MaxWaiters)
	}
	defer s.hub.leave(c.id)

	timeout := wp.Timeout
	if c.End == EndTimebox && c.EndsAt != nil {
		// Wake at the deadline so the expiry is reported promptly.
		timeout = min(timeout, max(c.EndsAt.Sub(s.now()), 0))
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var d Digest
	for {
		ch := s.hub.channel(c.id)
		d, err = s.digest(ctx, ns, name, wp.Agent, after)
		if err != nil {
			return Digest{}, err
		}
		if d.New > 0 || d.Standing.Status != StatusOpen {
			break
		}
		select {
		case <-ch:
			continue
		case <-timer.C:
			d, err = s.digest(ctx, ns, name, wp.Agent, after)
			if err != nil {
				return Digest{}, err
			}
			d.TimedOut = d.New == 0 && d.Standing.Status == StatusOpen
		case <-ctx.Done():
			return Digest{}, ctx.Err()
		}
		break
	}

	if err := s.touch(ctx, s.db, c, wp.Agent, PresenceWorking); err != nil {
		return Digest{}, err
	}
	return d, nil
}

// digest summarizes messages in (after, last_seq] as of one load of the
// conversation; every query is capped at that last_seq so the counts,
// previews and standing agree.
func (s *Service) digest(ctx context.Context, ns, name, agent string, after int64) (Digest, error) {
	c, err := s.loadConvRead(ctx, ns, name)
	if err != nil {
		return Digest{}, err
	}
	d := Digest{After: after, LastSeq: c.LastSeq}

	d.Authors, err = collect(ctx, s.db, func(rows *sql.Rows) (AuthorCount, error) {
		var ac AuthorCount
		err := rows.Scan(&ac.Author, &ac.Count)
		return ac, err
	}, `SELECT author, count(*) FROM messages
		WHERE conversation_id = ? AND seq > ? AND seq <= ? AND author != ?
		GROUP BY author ORDER BY min(seq)`, c.id, after, c.LastSeq, agent)
	if err != nil {
		return Digest{}, err
	}
	for _, ac := range d.Authors {
		d.New += ac.Count
	}

	d.Previews, err = collect(ctx, s.db, func(rows *sql.Rows) (Preview, error) {
		var pv Preview
		var body string
		err := rows.Scan(&pv.Seq, &pv.Kind, &pv.Author, &pv.ReplyTo, &body)
		pv.Text = previewText(body)
		return pv, err
	}, `SELECT seq, kind, author, reply_to, body FROM
		(SELECT * FROM messages WHERE conversation_id = ? AND seq > ? AND seq <= ? AND author != ?
		 ORDER BY seq DESC LIMIT ?) ORDER BY seq`, c.id, after, c.LastSeq, agent, maxPreviews)
	if err != nil {
		return Digest{}, err
	}

	d.Standing, err = s.standing(ctx, s.db, c, agent)
	return d, err
}

// previewText returns the first non-blank line of body, cut to
// maxPreviewText runes.
func previewText(body string) string {
	line := ""
	for l := range strings.SplitSeq(body, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			line = t
			break
		}
	}
	if utf8.RuneCountInString(line) <= maxPreviewText {
		return line
	}
	r := []rune(line)
	return string(r[:maxPreviewText-3]) + "..."
}

// Status returns the agent's standing in every open conversation in the
// namespace that it is still part of, plus open coordinator conversations it
// started (which it alone can close), most recently active first.
func (s *Service) Status(ctx context.Context, p Principal, ns, agent string) ([]Standing, error) {
	if err := s.authorize(ctx, p, ns, ActionRead); err != nil {
		return nil, err
	}
	if err := checkName("agent", agent); err != nil {
		return nil, err
	}
	nsID, err := namespaceID(ctx, s.db, ns)
	if err != nil {
		return nil, err
	}
	names, err := collect(ctx, s.db, func(rows *sql.Rows) (string, error) {
		var n string
		err := rows.Scan(&n)
		return n, err
	}, `SELECT c.name FROM conversations c
		WHERE c.namespace_id = ? AND c.status = 'open' AND (
			EXISTS (SELECT 1 FROM participants pt WHERE pt.conversation_id = c.id
				AND pt.agent = ? AND pt.state IN ('active', 'concluded'))
			OR (c.end_condition = 'coordinator' AND c.created_by = ?))
		ORDER BY c.last_at DESC, c.id DESC`, nsID, agent, agent)
	if err != nil {
		return nil, err
	}
	out := []Standing{}
	for _, n := range names {
		c, err := s.loadConvRead(ctx, ns, n)
		if err != nil {
			return nil, err
		}
		if c.Status != StatusOpen {
			continue // a timebox expired just now
		}
		st, err := s.standing(ctx, s.db, c, agent)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// StandingOf returns the agent's standing in one conversation, whether or
// not the agent has joined it.
func (s *Service) StandingOf(ctx context.Context, p Principal, ns, name, agent string) (Standing, error) {
	if err := s.authorize(ctx, p, ns, ActionRead); err != nil {
		return Standing{}, err
	}
	if err := checkName("agent", agent); err != nil {
		return Standing{}, err
	}
	c, err := s.loadConvRead(ctx, ns, name)
	if err != nil {
		return Standing{}, err
	}
	return s.standing(ctx, s.db, c, agent)
}
