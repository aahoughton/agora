package main

import "testing"

func TestShownNeutralizesTerminalControls(t *testing.T) {
	if got := shown("ok é\x1b]8;;x\x07\u202e\u0085\nend"); got != "ok é?]8;;x????end" {
		t.Fatalf("shown = %q", got)
	}
}
