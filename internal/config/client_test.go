package config

import (
	"flag"
	"io"
	"os"
	"path/filepath"
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
		{"token", []string{"--token", "t"}, map[string]string{EnvToken: "env"}, Client{URL: DefaultURL, Namespace: "default", Token: "t", TokenSource: "--token"}, ""},
		{"token from env", nil, map[string]string{EnvToken: "env"}, Client{URL: DefaultURL, Namespace: "default", Token: "env", TokenSource: EnvToken}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := flag.NewFlagSet("x", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			resolve := ClientFlags(fs, WithNamespace|WithToken)
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

func TestClientFlagsOnlyWhatIsAsked(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	resolve := ClientFlags(fs, 0)
	for _, name := range []string{"namespace", "token", "token-file"} {
		if fs.Lookup(name) != nil {
			t.Errorf("--%s is registered", name)
		}
	}
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	// An invalid namespace in the environment does not matter to a
	// command that has no namespace.
	got, err := resolve(env(map[string]string{EnvNamespace: "bad/name", EnvToken: "t"}))
	if err != nil || got != (Client{URL: DefaultURL}) {
		t.Fatalf("got %+v, %v", got, err)
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

func TestResolveToken(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := write("good", "file-token\n", 0o600)
	other := write("other", "other-token", 0o600)
	shared := write("shared", "t", 0o644)
	empty := write("empty", " \n", 0o600)
	two := write("two", "a b\n", 0o600)
	huge := write("huge", strings.Repeat("x", 4097), 0o600)
	xdg := filepath.Join(dir, "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "smoot"), 0o700); err != nil {
		t.Fatal(err)
	}
	write("xdg/smoot/token", "default-token", 0o600)
	missing := filepath.Join(dir, "missing")
	home, badHome := filepath.Join(dir, "home"), filepath.Join(dir, "bad-home")
	for _, h := range []string{home, badHome} {
		if err := os.MkdirAll(filepath.Join(h, ".config", "smoot"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("home/.config/smoot/token", "home-token\n", 0o600)
	write("bad-home/.config/smoot/token", "bad-token\n", 0o644)
	exact := write("exact", strings.Repeat("x", 4096), 0o600)

	tests := []struct {
		name      string
		flagToken string
		flagFile  string
		env       map[string]string
		want      string
		wantErr   string
	}{
		{"nothing", "", "", nil, "", ""},
		{"flag token", "t", "", map[string]string{EnvToken: "env", EnvTokenFile: other}, "t", ""},
		{"flag file beats env token", "", good, map[string]string{EnvToken: "env"}, "file-token", ""},
		{"both flags", "t", good, nil, "", "not both"},
		{"env token beats env file", "", "", map[string]string{EnvToken: "env", EnvTokenFile: good}, "env", ""},
		{"env file", "", "", map[string]string{EnvTokenFile: good}, "file-token", ""},
		{"env file beats default", "", "", map[string]string{EnvTokenFile: other, "XDG_CONFIG_HOME": xdg}, "other-token", ""},
		{"default under XDG_CONFIG_HOME", "", "", map[string]string{"XDG_CONFIG_HOME": xdg, "HOME": missing}, "default-token", ""},
		{"default missing is no token", "", "", map[string]string{"HOME": missing}, "", ""},
		{"default under HOME", "", "", map[string]string{"HOME": home}, "home-token", ""},
		{"relative XDG_CONFIG_HOME falls back to HOME", "", "", map[string]string{"XDG_CONFIG_HOME": "rel", "HOME": home}, "home-token", ""},
		{"bad default file", "", "", map[string]string{"HOME": badHome}, "", "chmod 600"},
		{"env token skips a bad default file", "", "", map[string]string{EnvToken: "env", "HOME": badHome}, "env", ""},
		{"flag token skips a bad env file", "t", "", map[string]string{EnvTokenFile: shared}, "t", ""},
		{"relative XDG_CONFIG_HOME is ignored", "", "", map[string]string{"XDG_CONFIG_HOME": "rel", "HOME": missing}, "", ""},
		{"too long", "", huge, nil, "", "over 4096 bytes"},
		{"exactly the limit", "", exact, nil, strings.Repeat("x", 4096), ""},
		{"named file missing", "", missing, nil, "", "no such file"},
		{"env file missing", "", "", map[string]string{EnvTokenFile: missing}, "", "no such file"},
		{"readable by others", "", shared, nil, "", "chmod 600"},
		{"empty", "", empty, nil, "", "is empty"},
		{"two words", "", two, nil, "", "more than one word"},
		{"directory", "", xdg, nil, "", "not a regular file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ResolveToken(tt.flagToken, tt.flagFile, env(tt.env))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestClientFlagsTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	resolve := ClientFlags(fs, WithToken)
	if err := fs.Parse([]string{"--token-file", path}); err != nil {
		t.Fatal(err)
	}
	got, err := resolve(env(map[string]string{EnvToken: "env"}))
	if err != nil || got.Token != "file-token" || got.TokenSource != "--token-file "+path {
		t.Fatalf("got %+v, %v", got, err)
	}
}
