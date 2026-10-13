package mcpserver

import (
	"errors"
	"strings"
	"testing"
)

func TestLostCallAdvice(t *testing.T) {
	err := errors.New("eof")
	tests := map[string]string{
		"read":   "after set to the last seq you actually saw",
		"say":    "may or may not have been applied",
		"status": "Retry the call.",
		"wait":   "after set to the last seq you actually saw",
	}
	for tool, want := range tests {
		if got := lostCallAdvice(tool, err); !strings.Contains(got, want) || !strings.Contains(got, "during "+tool) {
			t.Errorf("%s: %q", tool, got)
		}
	}
}
