package store

import (
	"path/filepath"
	"slices"
	"testing"
)

// Migration 8 rebuilds messages as a rowid table and indexes every message
// but system lines, then keeps the index current as messages are added.
func TestSearchMigrationKeepsMessagesAndIndexesThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agora.db")
	db, err := OpenAt(t.Context(), path, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO conversations (namespace_id, name, end_condition, created_by, created_at, last_seq, last_at)
			VALUES (1, 'c', 'none', 'alice', 1, 4, 1);
		INSERT INTO messages (conversation_id, seq, kind, author, body, created_at) VALUES
			(1, 1, 'system', 'alice', 'alice joined the migration talk', 1),
			(1, 2, 'say', 'alice', 'shall we migrate the messages table?', 2),
			(1, 3, 'observer', 'pat', 'Migration looks fine to me', 3);
		INSERT INTO messages (conversation_id, seq, kind, author, body, created_at, idempotency_key, seen_seq)
			VALUES (1, 4, 'conclude', 'alice', 'AGREED: rebuild it', 4, 'k1', 3)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	closeOnCleanup(t, db)
	ctx := t.Context()

	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("messages = %d, %v", n, err)
	}
	var key string
	var seen int
	if err := db.QueryRowContext(ctx, `SELECT idempotency_key, seen_seq FROM messages WHERE seq = 4`).Scan(&key, &seen); err != nil || key != "k1" || seen != 3 {
		t.Fatalf("seq 4 = %q %d, %v", key, seen, err)
	}
	for _, index := range []string{"messages_observers", "messages_idempotency"} {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = ?`, index).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s: %d, %v", index, n, err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages (conversation_id, seq, kind, author, body, created_at) VALUES (1, 4, 'say', 'bob', 'x', 5)`); err == nil {
		t.Error("a second message at seq 4 was accepted")
	}

	matches := func(q string) []int {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT m.seq FROM messages_fts f JOIN messages m ON m.id = f.rowid
			WHERE messages_fts MATCH ? ORDER BY m.seq`, q)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var seqs []int
		for rows.Next() {
			var s int
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			seqs = append(seqs, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return seqs
	}
	// Whole words, case-folded, no stemming; the system line is not indexed.
	if got := matches("migration"); !slices.Equal(got, []int{3}) {
		t.Errorf("migration matches %v", got)
	}
	if got := matches("migrat*"); !slices.Equal(got, []int{2, 3}) {
		t.Errorf("migrat* matches %v", got)
	}
	if got := matches("agreed"); !slices.Equal(got, []int{4}) {
		t.Errorf("agreed matches %v", got)
	}

	// New messages are indexed as they are added, system lines still not.
	if _, err := db.ExecContext(ctx, `INSERT INTO messages (conversation_id, seq, kind, author, body, created_at) VALUES
		(1, 5, 'say', 'bob', 'one more migration', 5), (1, 6, 'system', 'bob', 'bob left after the migration', 6)`); err != nil {
		t.Fatal(err)
	}
	if got := matches("migration"); !slices.Equal(got, []int{3, 5}) {
		t.Errorf("migration matches %v after inserts", got)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO messages_fts (messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Errorf("integrity-check: %v", err)
	}
}
