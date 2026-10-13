package core

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aahoughton/agora/internal/wording"
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
			WHERE conversation_id = ? AND seq > ? AND seq <= ? AND (author != ? OR kind = 'observer')`,
			c.id, pt.ReadSeq, c.LastSeq, agent).Scan(&st.Unread); err != nil {
			return Standing{}, err
		}
		// A reply to you answers one of your own posts; the replied-to
		// message is looked up by its primary key.
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM messages m
			WHERE m.conversation_id = ? AND m.seq > ? AND m.seq <= ? AND (m.author != ? OR m.kind = 'observer')
			AND EXISTS (SELECT 1 FROM messages p WHERE p.conversation_id = m.conversation_id AND p.seq = m.reply_to
				AND p.author = ? AND p.kind IN ('say', 'conclude'))`,
			c.id, pt.ReadSeq, c.LastSeq, agent, agent).Scan(&st.RepliesToYou); err != nil {
			return Standing{}, err
		}
	}
	canClose, err := s.creatorCanClose(ctx, q, c, agent)
	if err != nil {
		return Standing{}, err
	}

	var advice string // a wording phrase key
	switch {
	case c.Status == StatusConcluded && st.Unread > 0:
		st.Next, advice = NextRead, wording.AdviceReadAfterConclusion
	case c.Status == StatusConcluded:
		st.Next, advice = NextDone, wording.AdviceDone
	case canClose:
		st.Next, advice = NextClose, wording.AdviceClose
	case (st.You == YouLeft || st.You == YouNotJoined) && c.End == EndCoordinator && agent == c.CreatedBy:
		st.Next, advice = NextJoin, wording.AdviceCoordinatorAway
	case st.You == YouNotJoined:
		st.Next, advice = NextJoin, wording.AdviceJoin
	case st.You == YouLeft || st.You == YouDropped:
		st.Next, advice = NextJoin, wording.AdviceRejoin
	case st.Unread > DefaultReadLimit:
		// More than one read can carry: say how to catch up without
		// paging through it all.
		st.Next, advice = NextRead, wording.AdviceReadBacklog
	case st.Unread > 0:
		st.Next, advice = NextRead, wording.AdviceRead
	case st.You == YouConcluded:
		st.Next, advice = NextWait, wording.AdviceWaitConcluded
	case st.Expected:
		st.Next, advice = NextWait, wording.AdviceWaitExpected
	default:
		st.Next, advice = NextWait, wording.AdviceWaitFree
	}
	w := s.opts.Wording
	st.Line = w.Phrase(wording.StandingLine, map[string]any{
		"Conversation": c.Name, "Status": c.Status, "You": st.You, "Expected": st.Expected,
		"Unread": st.Unread, "Replies": st.RepliesToYou, "Next": st.Next, "Advice": w.Phrase(advice, nil),
	})
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

// Digest summarizes new activity. Normally it carries one-line previews and
// bodies come from Read. When the wait asked for inline delivery and the new
// messages are short, Messages holds them in full, they are marked read, and
// Previews is empty.
type Digest struct {
	After    int64         `json:"after"`
	LastSeq  int64         `json:"last_seq"`
	New      int           `json:"new"`
	Authors  []AuthorCount `json:"authors"`
	Previews []Preview     `json:"previews"`
	Messages []Message     `json:"messages,omitempty"`
	TimedOut bool          `json:"timed_out"`
	Standing Standing      `json:"standing"`
}

// maxInlineMessages bounds inline delivery by count as well as size.
const maxInlineMessages = 20

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
	// InlineBytes, if positive, asks for the new messages in full when their
	// bodies total at most this many bytes, saving the agent a read. Applies
	// only when After is unset.
	InlineBytes int
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
	err := s.writeAs(ctx, p, ns, ActionParticipate, func(tx *wtx) error {
		var err error
		c, err = s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		if err := s.checkOwner(ctx, tx, c, p, wp.Agent); err != nil {
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
	recheck := time.NewTicker(s.opts.RecheckEvery)
	defer recheck.Stop()

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
		case <-recheck.C:
			if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
				return Digest{}, err
			}
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
	// The caller may have lost access while blocked.
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return Digest{}, err
	}

	if wp.InlineBytes > 0 && wp.After == nil && d.New > 0 {
		if testHookBeforeInline != nil {
			testHookBeforeInline()
		}
		inlined, ok, err := s.inline(ctx, p, ns, name, wp.Agent, wp.InlineBytes)
		if err != nil {
			return Digest{}, err
		}
		if ok {
			inlined.TimedOut = false
			return inlined, nil
		}
	}
	if err := s.touch(ctx, s.db, c, wp.Agent, PresenceWorking); err != nil {
		return Digest{}, err
	}
	return d, nil
}

// testHookBeforeInline, set only by tests, runs just before inline delivery.
var testHookBeforeInline func()

// inline reads the agent's unread messages from others and returns them as a
// digest if they fit the budget, marking them read. ok is false, with nothing
// changed, when they do not fit.
func (s *Service) inline(ctx context.Context, p Principal, ns, name, agent string, budget int) (d Digest, ok bool, err error) {
	err = s.writeAs(ctx, p, ns, ActionParticipate, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		pt, err := s.member(ctx, tx, c, agent)
		if err != nil {
			return err
		}
		res, covered, err := s.page(ctx, tx, c, pt.ReadSeq, maxInlineMessages, agent)
		if err != nil {
			return err
		}
		size := 0
		for _, m := range res.Messages {
			size += len(m.Body)
		}
		if res.More || size > budget || len(res.Messages) == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE participants SET read_seq = max(read_seq, ?), presence = ?, last_seen_at = ?
			WHERE conversation_id = ? AND agent = ?`, covered, PresenceWorking, ms(s.now()), c.id, agent); err != nil {
			return err
		}
		d = Digest{After: pt.ReadSeq, LastSeq: c.LastSeq, New: len(res.Messages), Messages: res.Messages, Previews: []Preview{}}
		for _, m := range res.Messages {
			i := slices.IndexFunc(d.Authors, func(a AuthorCount) bool { return a.Author == m.Author })
			if i < 0 {
				d.Authors = append(d.Authors, AuthorCount{Author: m.Author})
				i = len(d.Authors) - 1
			}
			d.Authors[i].Count++
		}
		d.Standing, err = s.standing(ctx, tx, c, agent)
		ok = err == nil
		return err
	})
	return d, ok, err
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
		WHERE conversation_id = ? AND seq > ? AND seq <= ? AND (author != ? OR kind = 'observer')
		GROUP BY author ORDER BY min(seq)`, c.id, after, c.LastSeq, agent)
	if err != nil {
		return Digest{}, err
	}
	for _, ac := range d.Authors {
		d.New += ac.Count
	}

	d.Previews, err = s.previews(ctx, s.db, c, after, agent)
	if err != nil {
		return Digest{}, err
	}

	d.Standing, err = s.standing(ctx, s.db, c, agent)
	return d, err
}

// previews returns one-line previews of the latest maxPreviews messages
// from others in (after, c.LastSeq].
func (s *Service) previews(ctx context.Context, q querier, c *conv, after int64, agent string) ([]Preview, error) {
	return collect(ctx, q, func(rows *sql.Rows) (Preview, error) {
		var pv Preview
		var body string
		err := rows.Scan(&pv.Seq, &pv.Kind, &pv.Author, &pv.ReplyTo, &body)
		pv.Text = previewText(body)
		return pv, err
	}, `SELECT seq, kind, author, reply_to, body FROM
		(SELECT * FROM messages WHERE conversation_id = ? AND seq > ? AND seq <= ? AND (author != ? OR kind = 'observer')
		 ORDER BY seq DESC LIMIT ?) ORDER BY seq`, c.id, after, c.LastSeq, agent, maxPreviews)
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
				AND pt.agent = ? AND pt.state IN ('active', 'concluded')
				AND (? = 0 OR pt.account_id = ?))
			OR (c.end_condition = 'coordinator' AND c.created_by = ?
				AND (? = 0 OR c.created_by_account = ?)))
		ORDER BY c.last_at DESC, c.id DESC`, nsID, agent, p.AccountID, p.AccountID, agent, p.AccountID, p.AccountID)
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
	if err := s.checkOwner(ctx, s.db, c, p, agent); err != nil {
		return Standing{}, err
	}
	return s.standing(ctx, s.db, c, agent)
}
