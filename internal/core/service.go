// Package core implements namespaces, conversations and messages on top of
// the SQLite store. The JSON API, MCP server and HTML viewer all call it, and
// every method checks the caller's Principal against the Policy.
package core

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/aahoughton/smoot/internal/ratelimit"
	"github.com/aahoughton/smoot/internal/wording"
)

// Defaults for Options fields left zero.
const (
	DefaultMaxBodyBytes = 64 << 10
	DefaultArchiveAfter = 7 * 24 * time.Hour
	DefaultStaleAfter   = 10 * time.Minute
	DefaultMaxWaiters   = 64
	DefaultMaxWatchers  = 256
	DefaultRecheckEvery = time.Minute
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
	// MaxWatchers caps concurrent Follow calls (viewers) per conversation.
	MaxWatchers int
	// Wording supplies the advice in standing lines.
	Wording *wording.Wording
	// WriteLimit caps the writes (start, join, say, observe, conclude,
	// leave, close, presence and admin changes) each account may make per
	// minute; 0 is no limit. The anonymous principal (auth mode none) is
	// never limited.
	WriteLimit int
	// RecheckEvery is how often a blocked Wait or Follow re-checks that
	// its caller is still allowed, so revoking a credential or a
	// membership cuts it off. Both also re-check before returning.
	RecheckEvery time.Duration
	// Feedback lets agents leave notes on smoot itself (SubmitFeedback),
	// and invites them to in the standing line once they are free to stop.
	Feedback bool
}

// Service is the domain layer. It is safe for concurrent use.
type Service struct {
	db     *sql.DB
	opts   Options
	hub    hub
	writes *ratelimit.Limiter
	// writer admits one write transaction at a time, so this process's
	// writes queue here (in order, and cancelable) instead of spinning on
	// SQLite's lock.
	writer chan struct{}
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
	if opts.Wording == nil {
		opts.Wording = wording.Default()
	}
	if opts.MaxWatchers == 0 {
		opts.MaxWatchers = DefaultMaxWatchers
	}
	if opts.RecheckEvery == 0 {
		opts.RecheckEvery = DefaultRecheckEvery
	}
	s := &Service{db: db, opts: opts, writer: make(chan struct{}, 1), hub: hub{
		subs: map[int64]chan struct{}{}, waiters: map[int64]int{}, watchers: map[int64]int{},
	}}
	if r, ok := opts.Policy.(*Roles); ok {
		r.svc = s
	}
	if opts.WriteLimit > 0 {
		s.writes = ratelimit.New(opts.WriteLimit, time.Minute, 0, opts.Now)
	}
	return s
}

func (s *Service) now() time.Time { return s.opts.Now().UTC() }

func (s *Service) authorize(ctx context.Context, p Principal, ns string, a Action) error {
	return s.opts.Policy.Authorize(ctx, p, ns, a)
}

// authorizeIn is authorize reading through q, such as a write transaction.
func (s *Service) authorizeIn(ctx context.Context, q querier, p Principal, ns string, a Action) error {
	if _, ok := s.opts.Policy.(*Roles); ok {
		return s.authorizeRole(ctx, q, p, ns, a)
	}
	return s.opts.Policy.Authorize(ctx, p, ns, a)
}

// limitWrite takes one write from p's allowance.
func (s *Service) limitWrite(p Principal) error {
	if s.writes == nil || p.IsAnonymous() {
		return nil
	}
	if ok, wait := s.writes.Allow(strconv.FormatInt(p.AccountID, 10)); !ok {
		return RateLimited("writes", wait)
	}
	return nil
}

// busyRetryAfter is how soon a busy write is worth retrying.
const busyRetryAfter = time.Second

// busy turns SQLite's "database is locked" into CodeBusy.
func busy(err error) error {
	var se interface{ Code() int }
	if err == nil || !errors.As(err, &se) {
		return err
	}
	switch se.Code() & 0xff { // primary result code
	case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
		return &Error{Code: CodeBusy, Message: "the database is busy; retry in a moment", RetryAfter: busyRetryAfter}
	}
	return err
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
	// account is the account acting, recorded on what the transaction
	// appends; NULL for the anonymous principal and for the server itself.
	account sql.NullInt64
}

func (t *wtx) touched(convID int64) { t.changed = append(t.changed, convID) }

func (s *Service) write(ctx context.Context, fn func(tx *wtx) error) error {
	return s.writeChecked(ctx, Anonymous(), nil, fn)
}

// writeAs is write on behalf of p, whose account is recorded on appended
// messages and joined participants. Once it holds the write lock it checks
// again that p may do a in ns, so a revocation, disable or demotion that
// committed while the write waited for the lock wins.
func (s *Service) writeAs(ctx context.Context, p Principal, ns string, a Action, fn func(tx *wtx) error) error {
	return s.writeChecked(ctx, p, func(q querier) error { return s.authorizeIn(ctx, q, p, ns, a) }, fn)
}

// writeChecked is write on behalf of p with check run first, inside the
// transaction.
func (s *Service) writeChecked(ctx context.Context, p Principal, check func(querier) error, fn func(tx *wtx) error) error {
	select {
	case s.writer <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.writer }()
	return busy(s.runWrite(ctx, p, check, fn))
}

func (s *Service) runWrite(ctx context.Context, p Principal, check func(querier) error, fn func(tx *wtx) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tx := &wtx{Tx: sqlTx}
	if !p.IsAnonymous() {
		tx.account = sql.NullInt64{Int64: p.AccountID, Valid: true}
	}
	if check != nil {
		if err := check(tx); err != nil {
			_ = sqlTx.Rollback()
			return err
		}
	}
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
//
// Agents waiting (Wait) and viewers following (Follow) are counted and capped
// separately, so viewers cannot use up agents' wait slots.
type hub struct {
	mu       sync.Mutex
	subs     map[int64]chan struct{}
	waiters  map[int64]int
	watchers map[int64]int
}

// enter registers a waiter, refusing when max are already waiting.
func (h *hub) enter(convID int64, maxWaiters int) bool {
	return h.join(h.waiters, convID, maxWaiters)
}

// leave unregisters a waiter.
func (h *hub) leave(convID int64) { h.part(h.waiters, convID) }

// watch registers a follower, refusing when max are already following.
func (h *hub) watch(convID int64, maxWatchers int) bool {
	return h.join(h.watchers, convID, maxWatchers)
}

// unwatch unregisters a follower.
func (h *hub) unwatch(convID int64) { h.part(h.watchers, convID) }

func (h *hub) join(counts map[int64]int, convID int64, limit int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if counts[convID] >= limit {
		return false
	}
	counts[convID]++
	return true
}

// part decrements a count and drops the conversation's channel once nobody
// is waiting or following.
func (h *hub) part(counts map[int64]int, convID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	counts[convID]--
	if counts[convID] <= 0 {
		delete(counts, convID)
	}
	if h.waiters[convID] == 0 && h.watchers[convID] == 0 {
		delete(h.subs, convID)
	}
}

// channel returns the channel the next append will close. Only call it
// between enter and leave, or watch and unwatch.
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
