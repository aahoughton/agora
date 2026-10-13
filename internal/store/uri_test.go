package store

import (
	"os"
	"path/filepath"
	"testing"
)

// SQLite decodes %HH in a URI; a '%' in the path must name the same file.
func TestPercentInPathOpensThatFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a%2eb.db")
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO namespaces (name, created_at) VALUES ('x', 1)"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := os.Stat(filepath.Join(dir, "a.b.db")); !os.IsNotExist(err) {
		t.Fatalf("SQLite opened the decoded name: %v", err)
	}
	if v, err := Inspect(t.Context(), path); err != nil || v != LatestVersion() {
		t.Fatalf("inspect = %d, %v", v, err)
	}
}
