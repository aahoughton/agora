// Package ratelimit keeps a token bucket per key, for a bounded number of
// keys.
package ratelimit

import (
	"container/list"
	"sync"
	"time"
)

// DefaultMaxKeys bounds how many keys a Limiter tracks.
const DefaultMaxKeys = 10000

// Limiter allows each key up to Limit events per Per, in bursts of up to
// Limit. It forgets the least recently used keys beyond MaxKeys, which
// gives a forgotten key a full bucket again.
type Limiter struct {
	limit   float64
	per     time.Duration
	maxKeys int
	now     func() time.Time

	mu    sync.Mutex
	keys  map[string]*list.Element
	order *list.List // front is the most recently used
}

type bucket struct {
	key    string
	tokens float64
	at     time.Time
}

// New returns a limiter of limit events per per. now may be nil for the
// real clock.
func New(limit int, per time.Duration, maxKeys int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	if maxKeys <= 0 {
		maxKeys = DefaultMaxKeys
	}
	return &Limiter{
		limit: float64(limit), per: per, maxKeys: maxKeys, now: now,
		keys: map[string]*list.Element{}, order: list.New(),
	}
}

// Allow takes one event for key. If none is left it returns false and how
// long until one is.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	return l.take(key, true)
}

// Check reports whether key has an event left, without taking it.
func (l *Limiter) Check(key string) (bool, time.Duration) {
	return l.take(key, false)
}

func (l *Limiter) take(key string, consume bool) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.get(key, now)
	b.tokens = min(l.limit, b.tokens+now.Sub(b.at).Seconds()*l.limit/l.per.Seconds())
	b.at = now
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) * float64(l.per) / l.limit)
		return false, wait
	}
	if consume {
		b.tokens--
	}
	return true, 0
}

func (l *Limiter) get(key string, now time.Time) *bucket {
	if e, ok := l.keys[key]; ok {
		l.order.MoveToFront(e)
		return e.Value.(*bucket)
	}
	if l.order.Len() >= l.maxKeys {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		delete(l.keys, oldest.Value.(*bucket).key)
	}
	b := &bucket{key: key, tokens: l.limit, at: now}
	l.keys[key] = l.order.PushFront(b)
	return b
}
