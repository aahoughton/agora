package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aahoughton/agora/internal/client"
	"github.com/aahoughton/agora/internal/config"
	"github.com/aahoughton/agora/internal/wording"
)

// hookTimeout bounds the hook's requests: a client waits on its hooks.
const hookTimeout = 5 * time.Second

// defaultPatience is how many times in a row the hook blocks a stop while
// the agent makes no progress in the conversations it is expected in.
const defaultPatience = 3

// hookStateTTL is how long a session's hook state is kept unused.
const hookStateTTL = 24 * time.Hour

// hookState is what the hook remembers about one client session between
// stops: the progress marks of its expected conversations at the last
// block, and how many blocks in a row have seen the same marks.
type hookState struct {
	Progress string `json:"progress"`
	Blocks   int    `json:"blocks"`
}

func runHook(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv config.Getenv) int {
	// A client reads exit 2 from a Stop hook as "do not stop", so a
	// mistake in the hook's command line exits 1 instead: the session may
	// stop and the client shows the error.
	if code, ok := groupUsage(args, "stop", "usage: agora hook stop [--agent NAME] [flags]\n", stdout, stderr); !ok {
		return min(code, exitError)
	}
	fs := flag.NewFlagSet("hook stop", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `usage: agora hook stop [--agent NAME] [flags]

A Stop hook for Claude Code and Codex (see agora mcp-config --stop-hook).
It reads the hook's JSON on stdin and, while the agent is expected in an
open conversation, tells the client to continue with the agent's status
lines as the reason. It keeps holding a session while it makes progress
(the agent calls a tool in the conversation, such as wait, or a message
arrives), and lets it stop once it has blocked --patience times in a row
with no progress. It keeps that count per session in
$XDG_CACHE_HOME/agora/stop-hook (default ~/.cache). It lets the session
stop when no agent name is set or the server cannot be reached, so it
never traps a session.

`)
		fs.PrintDefaults()
	}
	resolveClient := config.ClientFlags(fs, config.WithNamespace|config.WithToken)
	agentFlag := fs.String("agent", "", "your agent name (env "+config.EnvAgent+"; without one the hook does nothing)")
	patience := fs.Int("patience", defaultPatience, "blocks in a row with no progress before the session may stop (1-20)")
	if err := fs.Parse(args[1:]); err != nil {
		return min(flagError(stderr, err), exitError)
	}
	if fs.NArg() > 0 {
		return min(flagError(stderr, fmt.Errorf("unexpected argument %q", fs.Arg(0))), exitError)
	}
	if *patience < 1 || *patience > 20 {
		return min(flagError(stderr, errors.New("--patience must be between 1 and 20")), exitError)
	}
	var in struct {
		SessionID      string `json:"session_id"`
		StopHookActive bool   `json:"stop_hook_active"`
	}
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in); err != nil {
		fmt.Fprintln(stderr, "agora hook stop: unreadable hook input:", err)
		return exitOK
	}
	agent := *agentFlag
	if agent == "" {
		agent = getenv(config.EnvAgent)
	}
	if agent == "" {
		return exitOK
	}
	// Without a session to remember, fall back to the client's own guard:
	// block once, then let the continued turn stop.
	statePath := ""
	if in.SessionID != "" {
		dir, err := hookStateDir(getenv)
		if err != nil {
			fmt.Fprintln(stderr, "agora hook stop:", err)
		} else {
			statePath = filepath.Join(dir, sessionFile(in.SessionID))
		}
	}
	if statePath == "" && in.StopHookActive {
		return exitOK
	}
	cc, err := resolveClient(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "agora hook stop:", err)
		return exitError
	}
	cl, err := client.New(cc.URL, cc.Token)
	if err != nil {
		fmt.Fprintln(stderr, "agora hook stop:", err)
		return exitError
	}
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	sts, err := cl.Status(ctx, cc.Namespace, agent)
	if err != nil {
		fmt.Fprintln(stderr, "agora hook stop:", err)
		return exitOK
	}
	var lines, progress []string
	for _, st := range sts {
		if !st.Expected {
			continue
		}
		lines = append(lines, st.Line)
		c, err := cl.GetConversation(ctx, st.Namespace, st.Conversation)
		if err != nil {
			fmt.Fprintln(stderr, "agora hook stop:", err)
			return exitOK
		}
		// Progress is a new message, or the agent calling in (any tool call
		// it makes in the conversation, such as wait, moves last_seen_at).
		mark := fmt.Sprintf("%s/%s@%d", st.Namespace, st.Conversation, c.LastSeq)
		for _, pt := range c.Participants {
			if pt.Agent == agent {
				mark += fmt.Sprintf(" read=%d seen=%d", pt.ReadSeq, pt.LastSeenAt.UnixMilli())
			}
		}
		progress = append(progress, mark)
	}
	if len(lines) == 0 {
		if statePath != "" {
			_ = os.Remove(statePath)
		}
		return exitOK
	}
	if statePath != "" {
		st := readHookState(statePath)
		p := strings.Join(progress, "\n")
		if st.Progress == p {
			st.Blocks++
		} else {
			st = hookState{Progress: p, Blocks: 1}
		}
		if err := writeHookState(statePath, st); err != nil {
			fmt.Fprintln(stderr, "agora hook stop:", err)
			if in.StopHookActive {
				return exitOK
			}
		}
		if st.Blocks > *patience {
			fmt.Fprintf(stderr, "agora hook stop: no progress in %d stops; letting the session stop\n", *patience)
			return exitOK
		}
	}
	reason := wording.Default().Phrase(wording.StopHook, map[string]any{"Lines": strings.Join(lines, "\n")})
	out, err := json.Marshal(map[string]string{"decision": "block", "reason": reason})
	if err != nil {
		fmt.Fprintln(stderr, "agora hook stop:", err)
		return exitOK
	}
	fmt.Fprintln(stdout, string(out))
	return exitOK
}

// hookStateDir is $XDG_CACHE_HOME/agora/stop-hook, falling back to
// $HOME/.cache/agora/stop-hook.
func hookStateDir(getenv config.Getenv) (string, error) {
	base := getenv("XDG_CACHE_HOME")
	if base == "" {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("no XDG_CACHE_HOME or HOME for the hook state")
		}
		base = filepath.Join(home, ".cache")
	} else if !filepath.IsAbs(base) {
		return "", fmt.Errorf("XDG_CACHE_HOME must be absolute, got %q", base)
	}
	return filepath.Join(base, "agora", "stop-hook"), nil
}

// sessionFile names a session's state file without putting the client's
// session id in a path.
func sessionFile(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:12]) + ".json"
}

// readHookState returns the saved state, or the zero state if there is none
// or it is unreadable.
func readHookState(path string) hookState {
	var st hookState
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &st) != nil {
		return hookState{}
	}
	return st
}

// writeHookState saves st and removes other sessions' state left unused
// for hookStateTTL.
func writeHookState(path string, st hookState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	pruneHookState(dir, path)
	return nil
}

func pruneHookState(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if p == keep || e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > hookStateTTL {
			_ = os.Remove(p)
		}
	}
}
