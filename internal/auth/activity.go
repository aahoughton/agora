package auth

import (
	"cmp"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aahoughton/agora/internal/core"
)

// Activity remembers who has called the server since it started and which
// addresses have presented credentials that were rejected, so an operator
// can tell whether a restart would interrupt anyone. It is held in memory
// and bounded: past the caps, the entries seen longest ago are dropped. A
// nil *Activity records nothing.
type Activity struct {
	proxies Proxies
	now     func() time.Time
	started time.Time

	mu       sync.Mutex
	callers  map[callerKey]*Caller
	failures map[string]*Failure
}

// Activity caps. One account can hold at most MaxCallersPerAccount entries,
// so varying its User-Agent cannot push other accounts out.
const (
	MaxCallers           = 512
	MaxCallersPerAccount = 32
	MaxFailures          = 256
	// maxClient bounds the User-Agent kept for a caller.
	maxClient = 64
)

// Surfaces a caller can arrive through.
const (
	SurfaceAPI = "api"
	SurfaceMCP = "mcp"
	SurfaceWeb = "web"
)

// Caller is one account calling through one surface from one address with
// one client.
type Caller struct {
	AccountID int64     `json:"-"`
	Surface   string    `json:"surface"`
	Address   string    `json:"address"`
	Client    string    `json:"client"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Requests  int64     `json:"requests"`
}

// Failure is the rejected credentials from one address.
type Failure struct {
	Address string    `json:"address"`
	Count   int64     `json:"count"`
	LastAt  time.Time `json:"last_at"`
}

type callerKey struct {
	account                  int64
	surface, address, client string
}

// NewActivity returns an empty Activity. Client addresses are read through
// proxies; now defaults to time.Now.
func NewActivity(proxies Proxies, now func() time.Time) *Activity {
	if now == nil {
		now = time.Now
	}
	return &Activity{
		proxies: proxies, now: now, started: now().UTC(),
		callers: map[callerKey]*Caller{}, failures: map[string]*Failure{},
	}
}

// Started is when the Activity was created, which is when the server
// started.
func (a *Activity) Started() time.Time {
	if a == nil {
		return time.Time{}
	}
	return a.started
}

// Seen records an authenticated request from p through surface.
func (a *Activity) Seen(r *http.Request, p core.Principal, surface string) {
	if a == nil {
		return
	}
	k := callerKey{p.AccountID, surface, a.proxies.ClientIP(r), clientName(r.UserAgent())}
	now := a.now().UTC()
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.callers[k]
	if !ok {
		mine := map[callerKey]*Caller{}
		for ck, cc := range a.callers {
			if ck.account == k.account {
				mine[ck] = cc
			}
		}
		switch {
		case len(mine) >= MaxCallersPerAccount:
			delete(a.callers, oldestKey(mine, func(c *Caller) time.Time { return c.LastSeen }))
		case len(a.callers) >= MaxCallers:
			delete(a.callers, oldestKey(a.callers, func(c *Caller) time.Time { return c.LastSeen }))
		}
		c = &Caller{AccountID: k.account, Surface: k.surface, Address: k.address, Client: k.client, FirstSeen: now}
		a.callers[k] = c
	}
	c.LastSeen = now
	c.Requests++
}

// Failed records a rejected credential from the request's address.
func (a *Activity) Failed(r *http.Request) {
	if a == nil {
		return
	}
	addr := a.proxies.ClientIP(r)
	now := a.now().UTC()
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.failures[addr]
	if !ok {
		if len(a.failures) >= MaxFailures {
			delete(a.failures, oldestKey(a.failures, func(f *Failure) time.Time { return f.LastAt }))
		}
		f = &Failure{Address: addr}
		a.failures[addr] = f
	}
	f.Count++
	f.LastAt = now
}

// Snapshot returns copies of the callers and failures, most recent first.
func (a *Activity) Snapshot() ([]Caller, []Failure) {
	if a == nil {
		return nil, nil
	}
	a.mu.Lock()
	callers := make([]Caller, 0, len(a.callers))
	for _, c := range a.callers {
		callers = append(callers, *c)
	}
	failures := make([]Failure, 0, len(a.failures))
	for _, f := range a.failures {
		failures = append(failures, *f)
	}
	a.mu.Unlock()
	slices.SortFunc(callers, func(x, y Caller) int {
		return cmp.Or(y.LastSeen.Compare(x.LastSeen), cmp.Compare(x.AccountID, y.AccountID),
			cmp.Compare(x.Surface, y.Surface), cmp.Compare(x.Address, y.Address), cmp.Compare(x.Client, y.Client))
	})
	slices.SortFunc(failures, func(x, y Failure) int {
		return cmp.Or(y.LastAt.Compare(x.LastAt), cmp.Compare(x.Address, y.Address))
	})
	return callers, failures
}

// oldestKey is the key of the entry seen longest ago.
func oldestKey[K comparable, V any](m map[K]V, at func(V) time.Time) K {
	var oldest K
	var t time.Time
	first := true
	for k, v := range m {
		if first || at(v).Before(t) {
			oldest, t, first = k, at(v), false
		}
	}
	return oldest
}

// clientName is a User-Agent cut to printable ASCII and maxClient bytes.
func clientName(ua string) string {
	var b strings.Builder
	for _, r := range ua {
		if b.Len() >= maxClient {
			break
		}
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
