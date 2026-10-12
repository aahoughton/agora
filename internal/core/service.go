// Package core implements namespaces, conversations and messages on top of
// the SQLite store. The JSON API, MCP server and HTML viewer all call it, and
// every method checks the caller's Principal against the Policy.
package core

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

// Defaults for Options fields left zero.
const (
	DefaultMaxBodyBytes = 64 << 10
	DefaultArchiveAfter = 7 * 24 * time.Hour
	DefaultStaleAfter   = 10 * time.Minute
	DefaultMaxWaiters   = 64
)

// Options configure a Service. Zero values select the defaults.
type Options struct {
	Policy       Policy
	Now          func() time.Time
	MaxBodyBytes int
	// ArchiveAfter is how long a conversation can go without a message before
	// listings treat it as archived.
	ArchiveAfter time.Duration
	// StaleAfter is how long a participant can go without calling in before
	// they are reported as stale.
	StaleAfter time.Duration
	// MaxWaiters caps concurrent waits per conversation.
	MaxWaiters int
}

// Service is the domain layer. It is safe for concurrent use.
type Service struct {
	db   *sql.DB
	opts Options
	hub  hub
}

// New returns a Service over an opened store.
func New(db *sql.DB, opts Options) *Service {
	if opts.Policy == nil {
		opts.Policy = AllowAll{}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if opts.ArchiveAfter == 0 {
		opts.ArchiveAfter = DefaultArchiveAfter
	}
	if opts.StaleAfter == 0 {
		opts.StaleAfter = DefaultStaleAfter
	}
	if opts.MaxWaiters == 0 {
		opts.MaxWaiters = DefaultMaxWaiters
	}
	return &Service{db: db, opts: opts, hub: hub{subs: map[int64]chan struct{}{}, waiters: map[int64]int{}}}
}

func (s *Service) now() time.Time { return s.opts.Now().UTC() }

func (s *Service) authorize(ctx context.Context, p Principal, ns string, a Action) error {
	return s.opts.Policy.Authorize(ctx, p, ns, a)
}

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// wtx is a write transaction that records which conversations changed so
// waiters can be woken after commit.
type wtx struct {
	*sql.Tx
	changed []int64
}

func (t *wtx) touched(convID int64) { t.changed = append(t.changed, convID) }

func (s *Service) write(ctx context.Context, fn func(tx *wtx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tx := &wtx{Tx: sqlTx}
	if err := fn(tx); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return err
	}
	for _, id := range tx.changed {
		s.hub.notify(id)
	}
	return nil
}

// hub wakes waiters when a conversation's log changes. A waiter enters, then
// repeatedly takes the current channel, re-checks the log, and blocks on the
// channel; notify closes it and drops it, so no append is missed between the
// check and the block. Entries exist only while a conversation has waiters.
type hub struct {
	mu      sync.Mutex
	subs    map[int64]chan struct{}
	waiters map[int64]int
}

// enter registers a waiter, refusing when max are already waiting.
func (h *hub) enter(convID int64, maxWaiters int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waiters[convID] >= maxWaiters {
		return false
	}
	h.waiters[convID]++
	return true
}

// leave unregisters a waiter and drops the conversation's entries when it
// was the last.
func (h *hub) leave(convID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.waiters[convID]--
	if h.waiters[convID] <= 0 {
		delete(h.waiters, convID)
		delete(h.subs, convID)
	}
}

// channel returns the channel the next append will close. Only call it
// between enter and leave.
func (h *hub) channel(convID int64) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.subs[convID]
	if !ok {
		ch = make(chan struct{})
		h.subs[convID] = ch
	}
	return ch
}

func (h *hub) notify(convID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[convID]; ok {
		close(ch)
		delete(h.subs, convID)
	}
}

// collect runs a query and scans every row. It always closes the rows and
// reports scan, iteration and close errors.
func collect[T any](ctx context.Context, q querier, scan func(*sql.Rows) (T, error), query string, args ...any) (out []T, err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); err == nil {
			err = cerr
		}
	}()
	out = []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fromNullMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

func nullMS(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: ms(*t), Valid: true}
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
