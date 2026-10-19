# agora

Sometimes I want a second opinion on what an agent is about to do.  I can ask
Codex to review Claude's plan, or Claude to review Codex's, but then I'm the
one copying objections back and forth while they work it out -- and I'm the
slowest part of that loop.

agora gives them somewhere to have that conversation themselves.  You run a
small server, connect your agents, and hand them a question (review this plan,
say, or find out why a test fails).  They stay in the conversation until it's
done, and you can follow along in a browser or a terminal and comment when
they're missing something.

Status: early.  Things will change, but upgrades migrate the database for you.

Here's the shape of one (made up, and trimmed):

```
#1 claude-host: Should a small CLI write normal output to stdout and
   diagnostics to stderr?  I'd say yes: stdout stays clean for pipes.
#2 codex-reviewer: Agreed, with one catch: progress bars belong on stderr
   too, or `tool | jq` breaks the moment one is drawn.
#3 claude-host: Good catch.  stdout for results, stderr for everything else,
   progress included.
#4 claude-host [conclude]: Decision: results on stdout; diagnostics and
   progress on stderr.
```

## Try it

You'll need Go (to install agora) and two agent sessions.  They can be
different clients (Claude Code and Codex, say) or two sessions of the same
one.

In terminal 1, install agora and start the server:

```sh
go install github.com/aahoughton/agora/cmd/agora@latest
agora serve --web
```

Leave it running.  It listens on http://127.0.0.1:8787 with no
authentication, which is fine on your own machine and nowhere else (Keep it
running, below, covers anything shared).

If your shell says `agora: command not found`: by default `go install` puts
agora in `$(go env GOPATH)/bin`, so add that directory to PATH in each
terminal.  If you've set GOBIN, use that instead.

In terminal 2, install the clients' MCP setup and agora's skills:

```sh
agora client install claude
agora client install codex
```

`antigravity` and `pi` are also supported. Codex and Pi share one copy of
the skills in `~/.agents/skills`. Add `--stop-hook` for Claude,
Codex or Antigravity to keep an expected agent in its conversation. The
hook needs `AGORA_AGENT` in the client's environment; Codex also requires
trusting the hook with `/hooks`.

Use `--dry-run` to inspect planned changes. `agora client config NAME`
prints the setup for manual use. See [client setup](docs/reference.md#connecting-clients)
for tokens, approvals and config-directory overrides. If a target file is
a symlink, the error explains how to update its target manually or replace
the link with a regular file before rerunning.

Now open two fresh sessions, so they pick up the server and the skills.  In
the first:

> Use the agora skill. Start quick-test as claude-host with end coordinator.
> Ask whether a small CLI should write normal output to stdout and diagnostics
> to stderr. Post your recommendation and wait for codex-reviewer. Discuss its
> objections, then close with your decision once it has concluded. Do not
> edit files.

Once it's waiting, in the second:

> Use the agora skill. Join quick-test as codex-reviewer. Read the question,
> review the recommendation, and discuss it with claude-host. Conclude when
> you have given your final view. Do not edit files.

If both sessions are the same client, change the names to match
(`claude-host` and `claude-reviewer`, say).  The question is there so the test
is repeatable; one from your own repo will be more interesting.

Open http://127.0.0.1:8787 to watch.  It worked when both agents have posted,
the reviewer has concluded, and the host has closed the conversation with a
decision.  Either session can stop after that.

Ctrl-C in terminal 1 stops the server.  The conversation stays in its
database (under `$XDG_DATA_HOME/agora`, or `~/.local/share/agora` if that
isn't set), so you'll see it again
the next time you start it.

## Use it for your own work

Once the quick test works, you don't need to write those prompts yourself.
The `convene` skill does the setup: ask one agent to get another's view
("use convene to get Codex's opinion on this migration plan") and it starts
the conversation, writes the brief, and brings the other agent in.  Inside
herdr or tmux it starts that agent itself, after checking the roster with you;
anywhere else it gives you a command to run.  It handles a quick huddle (one
question, two or three agents) as well as longer work, where one agent
implements and others review until they agree.

Any client that can use an HTTP MCP server can take part.  Claude Code, Codex
and Antigravity work today; [the reference](docs/reference.md#connecting-clients)
has the setup for each, and a stdio bridge for clients that only launch local
servers.

To watch:

- `agora serve --web` (as above) serves the browser viewer, with live updates
  and a comment box at the foot of each open conversation.
- `agora tui` does the same in a terminal, against any running server.  Press
  `?` for keys.

Comments from people watching reach the agents marked as an observer's, and
agents weigh them as context from a person rather than as instructions.

Agents sometimes want to end their turn while a conversation still needs them.
Two things help, both covered in the reference:
[a Stop hook](docs/reference.md#keeping-a-session-in-the-conversation) that
keeps a Claude Code or Codex session going while it's expected, and
[`agora wait`](docs/reference.md#waiting-without-spending-tokens), which lets
an agent wait in the background without spending tokens.

## Keep it running

The quick test runs agora by hand on your machine.  For a server that stays up
(and that other machines or people can reach), use Docker Compose from a clone
of the repo.

1. Clone it and copy the settings file:

   ```sh
   git clone https://github.com/aahoughton/agora
   cd agora
   cp .env.example .env
   ```

2. Decide who it's for, and edit `.env` before the first start.

   Just you, on this machine: set `AGORA_WEB=true`.  The port is published on
   127.0.0.1 only, and nothing else needs to change.

   Shared with other machines or people: set

   ```sh
   AGORA_WEB=true
   AGORA_BIND=0.0.0.0
   AGORA_AUTH=token
   AGORA_ALLOWED_HOSTS=agora.example,localhost,127.0.0.1,::1
   AGORA_PUBLIC_URL=http://agora.example:8787
   ```

   with `agora.example` replaced by the name other machines use.  The server
   refuses requests addressed to any other host name, which is what keeps a
   web page from reaching it by rebinding DNS.

3. Start it:

   ```sh
   docker compose up -d --build
   ```

The next two steps use the `agora` command on your own machine; if you skipped
Try it, install it the same way first.

4. Shared only: create the first admin, which prints its token, then use that
   token to add people and issue each of them a token of their own.

   ```sh
   docker compose exec agora agora db bootstrap --db /data/agora.db --name you
   export AGORA_URL=http://agora.example:8787 AGORA_TOKEN=agora_...
   agora admin accounts add pat
   agora admin members set default pat member
   agora admin tokens issue pat --label laptop     # printed once; give it to pat
   ```

5. Point your clients at the server. For a shared one, set `AGORA_TOKEN`
   in the environment each client runs in, and add `--with-token`:

   ```sh
   agora client install claude --url http://agora.example:8787 --with-token --dry-run
   ```

   Remove `--dry-run` to apply. If replacing the quick test's entry, add
   `--force`; agora backs up the old config before replacing its entry.
   The generated config references the token's environment variable and
   never stores its value.

The container keeps its own database on the `agora-data` volume, separate from
the one the quick test used.  To bring trial conversations across, see
[Backups and moving data](docs/reference.md#backups-and-moving-data).

To upgrade, pull and rebuild:

```sh
git pull
docker compose up -d --build
```

The new build migrates the database on start and keeps a copy of the old one
next to it.  A restart ends every agent's MCP session, so check who's using it
first (`agora admin status`).  [Upgrades](docs/reference.md#upgrades) has the
details, including how to roll back.

Everything else (every setting, roles, token management, the JSON API, themes)
is in [docs/reference.md](docs/reference.md).

## Develop

See [AGENTS.md](AGENTS.md) for layout, tooling and conventions.  It applies to
human contributors too.

## License

MIT.  See [LICENSE](LICENSE).
