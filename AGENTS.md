# AGENTS.md

Instructions for AI coding agents working in this repository. Agents are welcome
to contribute; the rules below apply to them and to humans alike.

## Commit attribution

Commits made with an AI assistant keep that tool's standard attribution trailer
(for Claude Code, its `Co-Authored-By:` line).

Tool-specific instruction files (`CLAUDE.md` and the like) are not committed. If
your tool needs one, create it locally, import or point at this file, and add it
to `.git/info/exclude`.

## Layout

```
cmd/agora/          the single binary and its subcommands
internal/config/    flag > env > default resolution
internal/store/     SQLite open, pragmas, embedded migrations
internal/core/      domain rules: namespaces, conversations, messages, attention
internal/auth/      request -> principal, and the policy for each auth mode
internal/api/       /v1 JSON API and its OpenAPI spec (openapi.yaml)
internal/mcpserver/ MCP tools at /mcp, their text rendering, and the stdio bridge
internal/wording/   every piece of agent-facing text, as embedded Markdown files
internal/client/    Go client for the JSON API (CLI and TUI use it)
internal/web/       HTML viewer: handlers, theme loading, safe markdown
internal/tone/      name -> participant color, shared by the viewer and TUI
internal/server/    HTTP handler assembly
themes/default/     the built-in viewer theme (templates and static files)
```

Every surface (JSON API, MCP, HTML) calls `internal/core`, and every core method
takes a `core.Principal` and consults the `core.Policy`. Do not bypass core to
reach the database.

When changing the API, update `internal/api/openapi.yaml` in the same commit; a
test fails if routes and spec disagree.

MCP tool results are plain text that ends with the standing line. Keep them
short: agents pay for every token, and the last line is what they act on.

The viewer contains no markup in Go: pages, partials and UI text live in
`themes/default/`, and `themes/README.md` documents the data contract
(`web.TemplateAPI`). After a template change, run
`go test ./internal/web -update` and review the diff to
`internal/web/testdata/*.golden.html`.

Agent-facing wording (server instructions, tool and parameter descriptions,
status-line advice, result phrases) lives in `internal/wording/text/`, not in
Go code. The loader rejects missing or unknown keys, and the MCP server refuses
to start if `tools.md` and the registered tools disagree.
`internal/mcpserver/testdata/agent-text.golden` snapshots everything an agent
sees; after a wording change, run `go test ./internal/mcpserver -update` and
review the diff to that file alongside the change.

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
| Build | `mise run build` (writes `bin/agora`) |
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
