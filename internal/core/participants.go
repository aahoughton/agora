package core

import (
	"context"
	"database/sql"
	"time"
)

// Participant states.
const (
	StateActive    = "active"
	StateConcluded = "concluded"
	StateLeft      = "left"
	StateDropped   = "dropped"
)

// Presence values a participant can report.
const (
	PresenceWorking = "working"
	PresenceWaiting = "waiting"
	PresenceAway    = "away"
)

// Participant is an agent's membership in a conversation.
type Participant struct {
	Agent       string     `json:"agent"`
	State       string     `json:"state"`
	JoinedAt    time.Time  `json:"joined_at"`
	ReadSeq     int64      `json:"read_seq"`
	Presence    string     `json:"presence"`
	Note        string     `json:"note,omitempty"`
	CheckBackBy *time.Time `json:"check_back_by,omitempty"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	// Stale is true when an active or concluded participant has not called in
	// for longer than Options.StaleAfter.
	Stale bool `json:"stale"`
}

const participantColumns = `agent, state, joined_at, read_seq, presence, note, check_back_by, last_seen_at`

func (s *Service) scanParticipant(row interface{ Scan(...any) error }) (Participant, error) {
	var pt Participant
	var joined, seen int64
	var checkBack sql.NullInt64
	if err := row.Scan(&pt.Agent, &pt.State, &joined, &pt.ReadSeq, &pt.Presence, &pt.Note, &checkBack, &seen); err != nil {
		return Participant{}, err
	}
	pt.JoinedAt = fromMS(joined)
	pt.LastSeenAt = fromMS(seen)
	pt.CheckBackBy = fromNullMS(checkBack)
	inConversation := pt.State == StateActive || pt.State == StateConcluded
	pt.Stale = inConversation && s.now().Sub(pt.LastSeenAt) > s.opts.StaleAfter
	return pt, nil
}

func (s *Service) participants(ctx context.Context, q querier, convID int64) ([]Participant, error) {
	return collect(ctx, q, func(rows *sql.Rows) (Participant, error) { return s.scanParticipant(rows) },
		`SELECT `+participantColumns+` FROM participants WHERE conversation_id = ? ORDER BY joined_at, agent`, convID)
}

// participant returns the agent's row, or nil if the agent never joined.
func (s *Service) participant(ctx context.Context, q querier, convID int64, agent string) (*Participant, error) {
	row := q.QueryRowContext(ctx, `SELECT `+participantColumns+`
		FROM participants WHERE conversation_id = ? AND agent = ?`, convID, agent)
	pt, err := s.scanParticipant(row)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pt, nil
}

// member returns the agent's row if they are active or concluded, and a
// not_participant error otherwise.
func (s *Service) member(ctx context.Context, q querier, c *conv, agent string) (*Participant, error) {
	pt, err := s.participant(ctx, q, c.id, agent)
	if err != nil {
		return nil, err
	}
	if pt == nil || (pt.State != StateActive && pt.State != StateConcluded) {
		return nil, errorf(CodeNotParticipant, "%s is not in conversation %q; join it first", agent, c.Name)
	}
	return pt, nil
}

func (s *Service) addParticipant(ctx context.Context, tx *wtx, c *conv, agent string) error {
	now := ms(s.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO participants
		(conversation_id, agent, state, joined_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
		c.id, agent, StateActive, now, now); err != nil {
		return err
	}
	_, err := s.appendMsg(ctx, tx, c, KindSystem, agent, agent+" joined", nil)
	return err
}

func (s *Service) setState(ctx context.Context, tx *wtx, c *conv, agent, state, presence string) error {
	_, err := tx.ExecContext(ctx, `UPDATE participants SET state = ?, presence = ?, last_seen_at = ?
		WHERE conversation_id = ? AND agent = ?`, state, presence, ms(s.now()), c.id, agent)
	return err
}

func (s *Service) touch(ctx context.Context, q querier, c *conv, agent, presence string) error {
	_, err := q.ExecContext(ctx, `UPDATE participants SET presence = ?, last_seen_at = ?
		WHERE conversation_id = ? AND agent = ?`, presence, ms(s.now()), c.id, agent)
	return err
}

func openOrError(c *conv) error {
	if c.Status != StatusOpen {
		return errorf(CodeConversationConcluded, "conversation %q has concluded", c.Name)
	}
	return nil
}

// JoinParams describe a join.
type JoinParams struct {
	Agent string
	// Resume must be true to rejoin under a name that is still active or
	// concluded, for an agent returning after a restart.
	Resume bool
}

// Join adds an agent to a conversation, or returns a left or dropped agent to
// it.
func (s *Service) Join(ctx context.Context, p Principal, ns, name string, jp JoinParams) (Standing, error) {
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return Standing{}, err
	}
	if err := checkName("agent", jp.Agent); err != nil {
		return Standing{}, err
	}
	var st Standing
	if err := s.settle(ctx, ns, name); err != nil {
		return Standing{}, err
	}
	err := s.write(ctx, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		if err := openOrError(c); err != nil {
			return err
		}
		pt, err := s.participant(ctx, tx, c.id, jp.Agent)
		if err != nil {
			return err
		}
		switch {
		case pt == nil:
			if err := s.addParticipant(ctx, tx, c, jp.Agent); err != nil {
				return err
			}
		case pt.State == StateActive || pt.State == StateConcluded:
			if !jp.Resume {
				return errorf(CodeNameActive, "%s is already in conversation %q; pass resume=true if you are %s returning, otherwise pick another name", jp.Agent, name, jp.Agent)
			}
			if err := s.touch(ctx, tx, c, jp.Agent, PresenceWorking); err != nil {
				return err
			}
			if _, err := s.appendMsg(ctx, tx, c, KindSystem, jp.Agent, jp.Agent+" resumed", nil); err != nil {
				return err
			}
		default:
			if err := s.setState(ctx, tx, c, jp.Agent, StateActive, PresenceWorking); err != nil {
				return err
			}
			if _, err := s.appendMsg(ctx, tx, c, KindSystem, jp.Agent, jp.Agent+" rejoined", nil); err != nil {
				return err
			}
		}
		st, err = s.standing(ctx, tx, c, jp.Agent)
		return err
	})
	return st, err
}

// LeaveParams describe a leave.
type LeaveParams struct {
	Agent string
	// Body, if set, is posted as a final message before leaving.
	Body string
}

// Leave removes an agent from a conversation. Leaving while expected (the
// conversation is open, has an end condition, and the agent has not
// concluded) is recorded as dropped, except for the creator, who may step
// away. Leaving a concluded conversation changes nothing and returns the
// standing; a final message with it is rejected.
func (s *Service) Leave(ctx context.Context, p Principal, ns, name string, lp LeaveParams) (Standing, error) {
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return Standing{}, err
	}
	if err := checkName("agent", lp.Agent); err != nil {
		return Standing{}, err
	}
	if err := s.checkBody(lp.Body, true); err != nil {
		return Standing{}, err
	}
	var st Standing
	if err := s.settle(ctx, ns, name); err != nil {
		return Standing{}, err
	}
	err := s.write(ctx, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		pt, err := s.member(ctx, tx, c, lp.Agent)
		if err != nil {
			return err
		}
		if c.Status != StatusOpen {
			// The log is closed and nobody is expected: nothing to record.
			if lp.Body != "" {
				return openOrError(c)
			}
			st, err = s.standing(ctx, tx, c, lp.Agent)
			return err
		}
		if lp.Body != "" {
			// A final message is a post like any other, so it withdraws a
			// conclusion first.
			if pt.State == StateConcluded {
				if _, err := s.appendMsg(ctx, tx, c, KindSystem, lp.Agent, lp.Agent+" withdrew their conclusion", nil); err != nil {
					return err
				}
				pt.State = StateActive
			}
			if _, err := s.appendMsg(ctx, tx, c, KindSay, lp.Agent, lp.Body, nil); err != nil {
				return err
			}
		}
		state, note := StateLeft, " left"
		if expected(c, pt) && lp.Agent != c.CreatedBy {
			state, note = StateDropped, " dropped out before the conversation concluded"
		}
		if err := s.setState(ctx, tx, c, lp.Agent, state, PresenceAway); err != nil {
			return err
		}
		if _, err := s.appendMsg(ctx, tx, c, KindSystem, lp.Agent, lp.Agent+note, nil); err != nil {
			return err
		}
		if err := s.checkAgreement(ctx, tx, c); err != nil {
			return err
		}
		st, err = s.standing(ctx, tx, c, lp.Agent)
		return err
	})
	return st, err
}

// expected reports whether the conversation is waiting on this participant.
func expected(c *conv, pt *Participant) bool {
	return c.Status == StatusOpen && c.End != EndNone && pt != nil && pt.State == StateActive
}

// checkAgreement concludes an agreement conversation once nobody is active
// and at least one participant has concluded.
func (s *Service) checkAgreement(ctx context.Context, tx *wtx, c *conv) error {
	if c.Status != StatusOpen || c.End != EndAgreement {
		return nil
	}
	var active, concluded int
	err := tx.QueryRowContext(ctx, `SELECT
		coalesce(sum(state = 'active'), 0), coalesce(sum(state = 'concluded'), 0)
		FROM participants WHERE conversation_id = ?`, c.id).Scan(&active, &concluded)
	if err != nil {
		return err
	}
	if active == 0 && concluded > 0 {
		return s.concludeConv(ctx, tx, c, "every remaining participant concluded")
	}
	return nil
}

// PresenceParams update an agent's presence.
type PresenceParams struct {
	Agent       string
	Presence    string
	Note        string
	CheckBackBy *time.Time
}

// SetPresence records what an agent is doing and when it will check back.
func (s *Service) SetPresence(ctx context.Context, p Principal, ns, name string, pp PresenceParams) (Participant, error) {
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return Participant{}, err
	}
	if err := checkName("agent", pp.Agent); err != nil {
		return Participant{}, err
	}
	switch pp.Presence {
	case PresenceWorking, PresenceWaiting, PresenceAway:
	default:
		return Participant{}, errorf(CodeInvalid, "invalid presence %q: want working, waiting or away", pp.Presence)
	}
	if len(pp.Note) > 500 {
		return Participant{}, errorf(CodeInvalid, "note is longer than 500 bytes")
	}
	var out Participant
	if err := s.settle(ctx, ns, name); err != nil {
		return Participant{}, err
	}
	err := s.write(ctx, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		if _, err := s.member(ctx, tx, c, pp.Agent); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE participants
			SET presence = ?, note = ?, check_back_by = ?, last_seen_at = ?
			WHERE conversation_id = ? AND agent = ?`,
			pp.Presence, pp.Note, nullMS(pp.CheckBackBy), ms(s.now()), c.id, pp.Agent); err != nil {
			return err
		}
		pt, err := s.participant(ctx, tx, c.id, pp.Agent)
		if err != nil {
			return err
		}
		out = *pt
		return nil
	})
	return out, err
}

// ConcludeParams record an agent's final position.
type ConcludeParams struct {
	Agent string
	Body  string
}

// Conclude records the agent's final position and marks them concluded.
// Posting again later withdraws the conclusion.
func (s *Service) Conclude(ctx context.Context, p Principal, ns, name string, cp ConcludeParams) (Standing, error) {
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return Standing{}, err
	}
	if err := checkName("agent", cp.Agent); err != nil {
		return Standing{}, err
	}
	if err := s.checkBody(cp.Body, false); err != nil {
		return Standing{}, err
	}
	var st Standing
	if err := s.settle(ctx, ns, name); err != nil {
		return Standing{}, err
	}
	err := s.write(ctx, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		if err := openOrError(c); err != nil {
			return err
		}
		if _, err := s.member(ctx, tx, c, cp.Agent); err != nil {
			return err
		}
		if _, err := s.appendMsg(ctx, tx, c, KindConclude, cp.Agent, cp.Body, nil); err != nil {
			return err
		}
		if err := s.setState(ctx, tx, c, cp.Agent, StateConcluded, PresenceWaiting); err != nil {
			return err
		}
		if err := s.checkAgreement(ctx, tx, c); err != nil {
			return err
		}
		st, err = s.standing(ctx, tx, c, cp.Agent)
		return err
	})
	return st, err
}

// CloseParams close a conversation.
type CloseParams struct {
	Agent string
	// Body, if set, is recorded as the creator's conclusion.
	Body string
}

// Close concludes a conversation. Only its creator may close it.
func (s *Service) Close(ctx context.Context, p Principal, ns, name string, cp CloseParams) (Standing, error) {
	if err := s.authorize(ctx, p, ns, ActionParticipate); err != nil {
		return Standing{}, err
	}
	if err := checkName("agent", cp.Agent); err != nil {
		return Standing{}, err
	}
	if err := s.checkBody(cp.Body, true); err != nil {
		return Standing{}, err
	}
	var st Standing
	if err := s.settle(ctx, ns, name); err != nil {
		return Standing{}, err
	}
	err := s.write(ctx, func(tx *wtx) error {
		c, err := s.loadConvTx(ctx, tx, ns, name)
		if err != nil {
			return err
		}
		if err := openOrError(c); err != nil {
			return err
		}
		if cp.Agent != c.CreatedBy {
			return errorf(CodeNotCoordinator, "only %s, who started conversation %q, can close it", c.CreatedBy, name)
		}
		if cp.Body != "" {
			if _, err := s.appendMsg(ctx, tx, c, KindConclude, cp.Agent, cp.Body, nil); err != nil {
				return err
			}
		}
		if err := s.concludeConv(ctx, tx, c, "closed by "+cp.Agent); err != nil {
			return err
		}
		st, err = s.standing(ctx, tx, c, cp.Agent)
		return err
	})
	return st, err
}
