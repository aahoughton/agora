package config

import (
	"flag"
	"io"
	"strings"
	"testing"
)

func TestClientFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		want    Client
		wantErr string
	}{
		{"defaults", nil, nil, Client{URL: DefaultURL, Namespace: "default"}, ""},
		{"env", nil, map[string]string{EnvURL: "http://h:1", EnvNamespace: "team"}, Client{URL: "http://h:1", Namespace: "team"}, ""},
		{"flag beats env", []string{"--url", "http://f:2", "--namespace", "f"}, map[string]string{EnvURL: "http://h:1", EnvNamespace: "team"}, Client{URL: "http://f:2", Namespace: "f"}, ""},
		{"bad namespace", []string{"--namespace", "../x"}, nil, Client{}, "invalid namespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("x", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			resolve := ClientFlags(fs)
			if err := fs.Parse(tt.args); err != nil {
				t.Fatal(err)
			}
			got, err := resolve(env(tt.env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestAgentFlag(t *testing.T) {
	for _, tt := range []struct {
		args    []string
		env     map[string]string
		want    string
		wantErr string
	}{
		{nil, nil, "", "agent name is required"},
		{nil, map[string]string{EnvAgent: "env-agent"}, "env-agent", ""},
		{[]string{"--agent", "flag-agent"}, map[string]string{EnvAgent: "env-agent"}, "flag-agent", ""},
		{[]string{"--agent", "bad name"}, nil, "", "invalid agent name"},
	} {
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		resolve := AgentFlag(fs)
		if err := fs.Parse(tt.args); err != nil {
			t.Fatal(err)
		}
		got, err := resolve(env(tt.env))
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("args %v: err = %v", tt.args, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("args %v: got %q, %v", tt.args, got, err)
		}
	}
}
