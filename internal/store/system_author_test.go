package store

import (
	"path/filepath"
	"testing"
)

// Migration 9 renames the system author of old system messages and leaves
// agent messages alone.
func TestSystemAuthorMigrationRenamesOnlySystemLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smoot.db")
	db, err := OpenAt(t.Context(), path, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO conversations (namespace_id, name, end_condition, created_by, created_at, last_seq, last_at)
			VALUES (1, 'c', 'none', 'alice', 1, 3, 1);
		INSERT INTO messages (conversation_id, seq, kind, author, body, created_at) VALUES
			(1, 1, 'system', 'agora', 'conversation concluded: agreed', 1),
			(1, 2, 'system', 'alice', 'alice joined', 2),
			(1, 3, 'say', 'agora', 'an agent that took the old name', 3)`); err != nil {
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
	for seq, want := range map[int]string{1: "smoot", 2: "alice", 3: "agora"} {
		var got string
		if err := db.QueryRowContext(t.Context(), `SELECT author FROM messages WHERE seq = ?`, seq).Scan(&got); err != nil || got != want {
			t.Errorf("seq %d author = %q, %v; want %q", seq, got, err, want)
		}
	}
}
