package auth

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/aahoughton/smoot/internal/config"
	"github.com/aahoughton/smoot/internal/core"
)

func TestActivityRecordsCallers(t *testing.T) {
	clock := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	a := NewActivity(nil, func() time.Time { return clock })
	from := func(addr, ua string) *http.Request {
		r := request(t, "")
		r.RemoteAddr = addr + ":1234"
		r.Header.Set("User-Agent", ua)
		return r
	}
	alice := core.Principal{AccountID: 1}
	a.Seen(from("10.0.0.1", "claude-code/2.1"), alice, SurfaceMCP)
	clock = clock.Add(time.Minute)
	a.Seen(from("10.0.0.1", "claude-code/2.1"), alice, SurfaceMCP)
	a.Seen(from("10.0.0.2", "smoot-tui\x1b[31m"), core.Principal{AccountID: 2}, SurfaceAPI)

	callers, failures := a.Snapshot()
	if len(failures) != 0 {
		t.Fatalf("failures = %v", failures)
	}
	want := []Caller{
		{AccountID: 1, Surface: SurfaceMCP, Address: "10.0.0.1", Client: "claude-code/2.1", FirstSeen: clock.Add(-time.Minute), LastSeen: clock, Requests: 2},
		{AccountID: 2, Surface: SurfaceAPI, Address: "10.0.0.2", Client: "smoot-tui[31m", FirstSeen: clock, LastSeen: clock, Requests: 1},
	}
	if fmt.Sprint(callers) != fmt.Sprint(want) {
		t.Fatalf("callers:\n got %v\nwant %v", callers, want)
	}
	if !a.Started().Equal(clock.Add(-time.Minute)) {
		t.Fatalf("started = %v", a.Started())
	}
}

func TestActivityDropsTheOldestPastTheCap(t *testing.T) {
	clock := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	a := NewActivity(nil, func() time.Time { return clock })
	for i := range MaxCallers + 1 {
		clock = clock.Add(time.Second)
		r := request(t, "")
		r.RemoteAddr = fmt.Sprintf("10.0.%d.%d:1", i/256, i%256)
		a.Seen(r, core.Principal{AccountID: int64(i + 1)}, SurfaceAPI)
		a.Failed(r)
	}
	callers, failures := a.Snapshot()
	if len(callers) != MaxCallers || callers[len(callers)-1].Address != "10.0.0.1" {
		t.Fatalf("%d callers, oldest %v", len(callers), callers[len(callers)-1])
	}
	if len(failures) != MaxFailures || failures[0].Address != "10.0.2.0" {
		t.Fatalf("%d failures, newest %v", len(failures), failures[0])
	}
}

func TestLimitedRecordsRejectedCredentials(t *testing.T) {
	svc := service(t, config.AuthToken)
	if _, err := svc.Bootstrap(t.Context(), "root"); err != nil {
		t.Fatal(err)
	}
	act := NewActivity(nil, nil)
	authn := Limited{Next: Bearer{Service: svc}, Failures: NewFailures(1, nil, nil), Activity: act}
	from := func(addr, authz string) *http.Request {
		r := request(t, authz)
		r.RemoteAddr = addr + ":1234"
		return r
	}
	_, _ = authn.Authenticate(from("10.0.0.1", ""))                   // nothing presented
	_, _ = authn.Authenticate(from("10.0.0.2", "Bearer smoot_wrong")) // rejected
	_, err := authn.Authenticate(from("10.0.0.2", "Bearer smoot_wrong"))
	if core.CodeOf(err) != core.CodeRateLimited {
		t.Fatalf("second failure: %v", err)
	}
	_, failures := act.Snapshot()
	if len(failures) != 1 || failures[0].Address != "10.0.0.2" || failures[0].Count != 2 {
		t.Fatalf("failures = %v", failures)
	}

	// A nil Activity and nil Failures record and limit nothing.
	authn = Limited{Next: Bearer{Service: svc}}
	if _, err := authn.Authenticate(from("10.0.0.2", "Bearer smoot_wrong")); core.CodeOf(err) != core.CodeUnauthorized {
		t.Fatalf("unlimited: %v", err)
	}
}

func TestActivityCapsEachAccount(t *testing.T) {
	clock := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	a := NewActivity(nil, func() time.Time { return clock })
	r := request(t, "")
	r.RemoteAddr = "10.0.0.9:1"
	a.Seen(r, core.Principal{AccountID: 2}, SurfaceMCP)
	for i := range MaxCallers {
		clock = clock.Add(time.Second)
		r := request(t, "")
		r.RemoteAddr = "10.0.0.1:1"
		r.Header.Set("User-Agent", fmt.Sprint("ua-", i))
		a.Seen(r, core.Principal{AccountID: 1}, SurfaceAPI)
	}
	callers, _ := a.Snapshot()
	mine := 0
	other := false
	for _, c := range callers {
		switch c.AccountID {
		case 1:
			mine++
		case 2:
			other = true
		}
	}
	if mine != MaxCallersPerAccount || !other {
		t.Fatalf("account 1 holds %d entries, account 2 kept: %v", mine, other)
	}
}
