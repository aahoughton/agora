//go:build unix

package config

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadTokenFileRefusesFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// Without a writer, a blocking open would hang here.
	if _, err := ReadTokenFile(fifo); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v", err)
	}
}
