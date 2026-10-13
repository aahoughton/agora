package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// LastMessage is the newest entry in any conversation's log.
type LastMessage struct {
	Namespace    string    `json:"namespace"`
	Conversation string    `json:"conversation"`
	Seq          int64     `json:"seq"`
	Kind         string    `json:"kind"`
	Author       string    `json:"author"`
	At           time.Time `json:"at"`
}

// LiveConversation is a conversation someone may be using now.
type LiveConversation struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Status    Status    `json:"status"`
	LastAt    time.Time `json:"last_at"`
	// Active counts participants that have not concluded or left.
	Active int `json:"active"`
	// Waiting counts agents blocked in wait; Viewing counts viewers
	// following the conversation.
	Waiting int `json:"waiting"`
	Viewing int `json:"viewing"`
}

// MaxLiveConversations caps ServerActivity's conversation list.
const MaxLiveConversations = 100

// ServerActivity is the server's current use as the database and the
// waiters show it.
type ServerActivity struct {
	LastMessage *LastMessage `json:"last_message"`
	// Conversations are those open and active since the cutoff, and any
	// with agents waiting or viewers following, most recent first.
	Conversations []LiveConversation `json:"conversations"`
}

// ServerActivity reports the newest message and the conversations in use:
// open with activity since since, or with someone waiting or following.
// Server admins only.
func (s *Service) ServerActivity(ctx context.Context, p Principal, since time.Time) (ServerActivity, error) {
	if err := s.requireServerAdmin(ctx, p); err != nil {
		return ServerActivity{}, err
	}
	waiting, viewing := s.hub.counts()
	live := make([]int64, 0, len(waiting)+len(viewing))
	for id := range waiting {
		live = append(live, id)
	}
	for id := range viewing {
		if waiting[id] == 0 {
			live = append(live, id)
		}
	}
	ids, err := json.Marshal(live)
	if err != nil {
		return ServerActivity{}, err
	}

	var out ServerActivity
	var lm LastMessage
	var at int64
	err = s.db.QueryRowContext(ctx, `
		SELECT n.name, c.name, m.seq, m.kind, m.author, m.created_at
		FROM conversations c
		JOIN namespaces n ON n.id = c.namespace_id
		JOIN messages m ON m.conversation_id = c.id AND m.seq = c.last_seq
		ORDER BY c.last_at DESC, c.id DESC LIMIT 1`).
		Scan(&lm.Namespace, &lm.Conversation, &lm.Seq, &lm.Kind, &lm.Author, &at)
	switch {
	case err == nil:
		lm.At = fromMS(at)
		out.LastMessage = &lm
	case !isNoRows(err):
		return ServerActivity{}, err
	}

	type row struct {
		id int64
		c  LiveConversation
	}
	rows, err := collect(ctx, s.db, func(rs *sql.Rows) (row, error) {
		var r row
		var last int64
		err := rs.Scan(&r.id, &r.c.Namespace, &r.c.Name, &r.c.Status, &last, &r.c.Active)
		r.c.LastAt = fromMS(last)
		return r, err
	}, `
		SELECT c.id, n.name, c.name, c.status, c.last_at,
			(SELECT count(*) FROM participants p WHERE p.conversation_id = c.id AND p.state = 'active')
		FROM conversations c
		JOIN namespaces n ON n.id = c.namespace_id
		WHERE (c.status = 'open' AND c.last_at >= ?)
			OR c.id IN (SELECT value FROM json_each(?))
		ORDER BY c.last_at DESC, c.id DESC LIMIT ?`, ms(since), string(ids), MaxLiveConversations)
	if err != nil {
		return ServerActivity{}, err
	}
	out.Conversations = make([]LiveConversation, 0, len(rows))
	for _, r := range rows {
		r.c.Waiting, r.c.Viewing = waiting[r.id], viewing[r.id]
		out.Conversations = append(out.Conversations, r.c)
	}
	return out, nil
}

// counts copies the number of waiters and followers per conversation.
func (h *hub) counts() (waiters, watchers map[int64]int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	waiters = make(map[int64]int, len(h.waiters))
	for id, n := range h.waiters {
		waiters[id] = n
	}
	watchers = make(map[int64]int, len(h.watchers))
	for id, n := range h.watchers {
		watchers[id] = n
	}
	return waiters, watchers
}
