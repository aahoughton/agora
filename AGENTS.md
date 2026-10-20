# AGENTS.md

Instructions for AI coding agents working in this repository. Agents are welcome
to contribute; the rules below apply to them and to humans alike.

## Commit attribution

Commits made with an AI assistant keep that tool's standard attribution trailer
(for Claude Code, its `Co-Authored-By:` line).

Tool-specific instruction files (`CLAUDE.md` and the like) are not committed. If
your tool needs one, create it locally, import or point at this file, and add it
to `.git/info/exclude` anchored to the root (`/CLAUDE.md`): on a
case-insensitive checkout a bare `CLAUDE.md` also hides
`internal/profiles/text/claude.md`.

## Layout

```
cmd/smoot/          the single binary and its subcommands
internal/config/    flag > env > default resolution
internal/store/     SQLite open, pragmas, embedded migrations
internal/core/      domain rules: namespaces, conversations, messages, attention
internal/auth/      request -> principal (bearer tokens, session cookies), the policy for each auth mode, failed-auth limits, recent callers (in memory)
internal/ratelimit/ token buckets per key, bounded
internal/api/       /v1 JSON API and its OpenAPI spec (openapi.yaml)
internal/mcpserver/ MCP tools at /mcp, their text rendering, and the stdio bridge
internal/profiles/  client profiles, config editing and install preflight for smoot client
internal/wording/   every piece of agent-facing text, as embedded Markdown files
internal/textfile/  the "## key" section format of the text files
internal/client/    Go client for the JSON API (CLI and TUI use it)
internal/web/       HTML viewer: handlers, theme loading, safe markdown
internal/tui/       terminal viewer (smoot tui), a Bubble Tea client of the JSON API
internal/tone/      name -> participant color, shared by the viewer and TUI
internal/markdown/  Markdown dialect and link policy shared by the viewer and TUI
internal/server/    HTTP handler assembly
themes/default/     the built-in viewer theme (templates, static files, tui/)
skills/             agent skills (smoot, convene), embedded for client install and client skills
```

Every surface (JSON API, MCP, HTML) calls `internal/core`, and every core method
takes a `core.Principal` and consults the `core.Policy`. Do not bypass core to
reach the database.

When changing the API, update `internal/api/openapi.yaml` in the same commit; a
test fails if routes and spec disagree. Response bodies are snapshotted in
`internal/api/testdata/contract.golden`; after an intended change run
`go test ./internal/api -update` and review that diff with the spec.

MCP tool results are plain text that ends with the standing line. Keep them
short: agents pay for every token, and the last line is what they act on.
The exception is `feedback`, which does not change the agent's standing and
returns one line.

The viewer contains no markup in Go: pages, partials and UI text live in
`themes/default/`, and `themes/README.md` documents the data contract
(`web.TemplateAPI`). After a template change, run
`go test ./internal/web -update` and review the diff to
`internal/web/testdata/*.golden.html`.

The TUI follows the same rule: its strings, symbols and colors live in
`themes/default/tui/`, documented in `themes/README.md`. After a change to
its text or rendering, run `go test ./internal/tui -update` and review the
diff to `internal/tui/testdata/*.golden`.

Agent-facing wording (server instructions, tool and parameter descriptions,
status-line advice, result phrases) lives in `internal/wording/text/`, not in
Go code. The loader rejects missing or unknown keys, and the MCP server refuses
to start if `tools.md` and the registered tools disagree.
`internal/mcpserver/testdata/agent-text.golden` snapshots everything an agent
sees; after a wording change, run `go test ./internal/mcpserver -update` and
review the diff to that file alongside the change.

Client profiles in `internal/profiles/text/` define config roots, skills
paths and install recipes. Each install section starts with one JSON metadata
line followed by a Go value template. `~/` resolves under HOME; `@/` under
the profile config root, honoring `home_env`. `file_when_home_env` covers an
explicit alternate path. Keep client-specific setup in this data; Go owns
`json-set`, `json-append`, `json-ensure-strings`, `toml-table` and `command`.
Hook contracts are explicit adapters. Templates receive the token's env
variable name, never its value. After profile or rendering changes, run
`go test ./internal/profiles -update` and review `testdata/*.golden`.
Installer tests use temporary homes; never use real client configs.

To see how a real agent responds to the wording, run
`scripts/live-pair.sh codex|claude [output-dir]`. A scripted host asks a
question, pushes back once, and concludes after the agent does; the script
reports whether the agent joined, answered, stayed through the pushback,
concluded and stopped on its own, with its token use and the transcript. It
calls a paid model and is not deterministic, so it is a development aid and
not part of CI. Override `PROMPT` or `QUESTION` to try variations.

More packages arrive as the API, MCP, HTML and TUI surfaces land; keep this list
current when adding one.

## Tooling

Versions are pinned in `mise.toml`; run `mise install` once. Go dev tools that ship
as Go packages are pinned with `tool` directives in `go.mod` and run with
`go tool <name>`.

| Task | Command |
|---|---|
| Build | `mise run build` (writes `bin/smoot`) |
| Test | `mise run test` (`go test -race ./...`) |
| Lint | `mise run lint` |
| Format | `mise run fmt` |
| Everything CI runs | `mise run check` |

Install the git hooks once per clone with `mise exec -- lefthook install`. The
pre-commit hook runs gitleaks on staged changes, checks formatting, and lints.

## Conventions

- Tests use the standard `testing` package: table-driven cases with `t.Run`,
  `httptest` for HTTP, a real SQLite file in `t.TempDir()` for storage. No
  assertion libraries, no mocks of our own packages.
- Do not suppress lint findings inline. If a rule is wrong for this repo, change
  `.golangci.yml` with a comment saying why.
- Errors and log lines are ASCII, short, and lowercase in the Go style.
- Conventional commit subjects (`feat:`, `fix:`, `refactor:`, `test:`,
  `chore:`, `docs:`). Bodies say what changed and why, briefly.

## Public repository hygiene

This repository is public. Before committing, check that nothing personal or
secret is staged:

- No absolute paths from a developer machine, real names or emails outside
  LICENSE and git metadata, hostnames, or tokens.
- Test fixtures and examples are synthetic. Never copy real conversation data in.
- Databases, `.env` files and `data/` are ignored; keep it that way.
- A gitleaks finding blocks the commit. Fix the content; add an allowlist entry to
  `.gitleaks.toml` only for a reviewed false positive, with a comment.
