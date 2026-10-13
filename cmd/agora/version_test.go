package main

import "testing"

func TestBuildVersion(t *testing.T) {
	tests := []struct{ set, module, want string }{
		{"v1.2.3", "v0.0.0-20261013171918-4215ead0f5dc", "v1.2.3"}, // -ldflags wins
		{"dev", "v0.0.0-20261013171918-4215ead0f5dc", "v0.0.0-20261013171918-4215ead0f5dc"},
		{"dev", "(devel)", "dev"},
		{"dev", "", "dev"},
	}
	for _, tt := range tests {
		if got := buildVersion(tt.set, tt.module); got != tt.want {
			t.Errorf("buildVersion(%q, %q) = %q, want %q", tt.set, tt.module, got, tt.want)
		}
	}
}
