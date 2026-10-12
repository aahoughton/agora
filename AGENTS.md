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
internal/server/    HTTP handler assembly
```

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
