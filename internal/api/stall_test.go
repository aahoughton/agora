package api_test

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aahoughton/agora/internal/api"
	"github.com/aahoughton/agora/internal/auth"
	"github.com/aahoughton/agora/internal/core"
	"github.com/aahoughton/agora/internal/store"
)

// A dump holds a read transaction while it streams. A client that stops
// reading must not hold it for good: that keeps SQLite from checkpointing
// its write-ahead log, which then grows with every write on the server.
func TestStalledDumpReleasesTheDatabase(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "agora.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := core.New(db, core.Options{})
	p := core.Anonymous()
	if _, err := svc.Start(ctx, p, "default", core.StartParams{Name: "c", Agent: "alice", Join: true}); err != nil {
		t.Fatal(err)
	}
	// Far more than the socket buffers hold, so the dump blocks on write.
	body := strings.Repeat("x", 60<<10)
	for range 300 {
		if _, err := svc.Say(ctx, p, "default", "c", core.SayParams{Agent: "alice", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	api.Register(mux, svc, auth.None{}, api.Options{Logger: slog.New(slog.DiscardHandler), StreamWriteTimeout: 300 * time.Millisecond})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.(*net.TCPConn).SetReadBuffer(4 << 10)
	if _, err := conn.Write([]byte("GET /v1/dump?namespace=default HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // let the dump start and block
	if _, err := svc.Say(ctx, p, "default", "c", core.SayParams{Agent: "alice", Body: "after the dump began"}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		var busy, logFrames, done int
		err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &done)
		if err == nil && busy == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the write-ahead log still cannot be checkpointed (busy %d, err %v): the stalled dump holds its read transaction", busy, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
