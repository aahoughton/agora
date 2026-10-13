package config

import (
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

func TestParseServeDefaults(t *testing.T) {
	cfg, err := ParseServe(nil, env(map[string]string{"HOME": "/home/u"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := Serve{
		DB:         "/home/u/.local/share/agora/agora.db",
		Addr:       DefaultAddr,
		Web:        false,
		Auth:       AuthNone,
		MCPMaxWait: DefaultMCPMaxWait,
		LogLevel:   slog.LevelInfo,
		LogFormat:  "text",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestParseServeXDGDataHome(t *testing.T) {
	cfg, err := ParseServe(nil, env(map[string]string{"HOME": "/home/u", "XDG_DATA_HOME": "/xdg"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB != "/xdg/agora/agora.db" {
		t.Fatalf("DB = %q", cfg.DB)
	}
}

func TestParseServeRelativeXDGDataHome(t *testing.T) {
	_, err := ParseServe(nil, env(map[string]string{"HOME": "/home/u", "XDG_DATA_HOME": "rel"}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "XDG_DATA_HOME") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseServeNoHome(t *testing.T) {
	_, err := ParseServe(nil, env(nil), io.Discard)
	if err == nil || !strings.Contains(err.Error(), EnvDB) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseServeNoHomeButEnvDB(t *testing.T) {
	cfg, err := ParseServe(nil, env(map[string]string{EnvDB: "/data/agora.db"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB != "/data/agora.db" {
		t.Fatalf("DB = %q", cfg.DB)
	}
}

func TestParseServeEnvOverridesDefault(t *testing.T) {
	cfg, err := ParseServe(nil, env(map[string]string{
		"HOME":        "/home/u",
		EnvDB:         "/env.db",
		EnvAddr:       ":9000",
		EnvWeb:        "true",
		EnvAuth:       "none",
		EnvMCPMaxWait: "2m",
		EnvLogLevel:   "debug",
		EnvLogFormat:  "json",
	}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := Serve{
		DB: "/env.db", Addr: ":9000", Web: true, Auth: AuthNone, MCPMaxWait: 2 * time.Minute,
		LogLevel: slog.LevelDebug, LogFormat: "json",
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestParseServeFlagOverridesEnv(t *testing.T) {
	cfg, err := ParseServe(
		[]string{"--db", "/flag.db", "--addr", ":1", "--web=false"},
		env(map[string]string{EnvDB: "/env.db", EnvAddr: ":9000", EnvWeb: "true"}),
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DB != "/flag.db" || cfg.Addr != ":1" || cfg.Web {
		t.Fatalf("got %+v", cfg)
	}
}

func TestParseServeErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"bad web", []string{"--web=maybe"}, nil, "invalid boolean value"},
		{"bad web env", nil, map[string]string{EnvWeb: "yes please"}, "invalid AGORA_WEB value"},
		{"unknown auth", []string{"--auth", "ldap"}, nil, `unknown auth mode "ldap"`},
		{"token not yet", []string{"--auth", "token"}, nil, "not implemented yet"},
		{"stray arg", []string{"extra"}, nil, "unexpected argument"},
		{"bad mcp wait", []string{"--mcp-max-wait", "soon"}, nil, "invalid mcp-max-wait"},
		{"mcp wait too short", []string{"--mcp-max-wait", "10ms"}, nil, "invalid mcp-max-wait"},
		{"unknown flag", []string{"--nope"}, nil, "flag provided but not defined"},
		{"bad log level", []string{"--log-level", "loud"}, nil, "invalid log-level"},
		{"log level offset", nil, map[string]string{EnvLogLevel: "info+2"}, "invalid log-level"},
		{"bad log format", []string{"--log-format", "xml"}, nil, "invalid log-format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := map[string]string{"HOME": "/home/u"}
			for k, v := range tt.env {
				e[k] = v
			}
			_, err := ParseServe(tt.args, env(e), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseServeAllowedHosts(t *testing.T) {
	tests := []struct {
		args    []string
		env     string
		hosts   []string
		any     bool
		wantErr bool
	}{
		{nil, "", nil, false, false},
		{[]string{"--allowed-hosts", "*"}, "", nil, true, false},
		{nil, "agora.lan, localhost", []string{"agora.lan", "localhost"}, false, false},
		{[]string{"--allowed-hosts", "a.example"}, "b.example", []string{"a.example"}, false, false},
		{[]string{"--allowed-hosts", "a,*"}, "", nil, false, true},
		{[]string{"--allowed-hosts", "a,,b"}, "", nil, false, true},
	}
	for _, tt := range tests {
		cfg, err := ParseServe(tt.args, env(map[string]string{"HOME": "/h", EnvAllowedHosts: tt.env}), io.Discard)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%v %q: no error", tt.args, tt.env)
			}
			continue
		}
		if err != nil || cfg.AnyHost != tt.any || !slices.Equal(cfg.AllowedHosts, tt.hosts) {
			t.Errorf("%v %q: got %v %v, %v", tt.args, tt.env, cfg.AllowedHosts, cfg.AnyHost, err)
		}
	}
}

func TestParseServeWebIsABooleanFlag(t *testing.T) {
	cfg, err := ParseServe([]string{"--web", "--db", "/x.db"}, env(map[string]string{"HOME": "/h"}), io.Discard)
	if err != nil || !cfg.Web || cfg.DB != "/x.db" {
		t.Fatalf("--web --db = %+v, %v", cfg, err)
	}
	// An explicit flag beats the environment, in both directions.
	cfg, err = ParseServe([]string{"--web=false"}, env(map[string]string{"HOME": "/h", EnvWeb: "true"}), io.Discard)
	if err != nil || cfg.Web {
		t.Fatalf("--web=false with env true = %+v, %v", cfg, err)
	}
	cfg, err = ParseServe(nil, env(map[string]string{"HOME": "/h", EnvWeb: "true"}), io.Discard)
	if err != nil || !cfg.Web {
		t.Fatalf("env true = %+v, %v", cfg, err)
	}
}

func TestParseServeLogFlagOverridesEnv(t *testing.T) {
	cfg, err := ParseServe([]string{"--log-level", "WARN", "--log-format", "text"},
		env(map[string]string{"HOME": "/h", EnvLogLevel: "debug", EnvLogFormat: "json"}), io.Discard)
	if err != nil || cfg.LogLevel != slog.LevelWarn || cfg.LogFormat != "text" {
		t.Fatalf("got %v %q, %v", cfg.LogLevel, cfg.LogFormat, err)
	}
}
