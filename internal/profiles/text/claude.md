# Claude Code

The profile for Claude Code. Sections are Go text/templates; `fields:`
lists their data. See internal/profiles for what each key is for.

## description
Claude Code (claude)

## skills_dir
~/.claude/skills

## mcp_config
fields: MCPURL, MCPURLShell (quoted for a shell), MCPURLString (a quoted JSON/TOML string), TimeoutMS, TokenEnv, TokenEnvString
# Claude Code: add agora for every project (user scope).
claude mcp add --transport http --scope user agora {{.MCPURLShell}}{{if .TokenEnv}} --header 'Authorization: Bearer ${ {{- .TokenEnv -}} }'{{end}}
{{- if .TokenEnv}}
# Claude Code expands ${ {{- .TokenEnv -}} } when it connects; set {{.TokenEnv}} in the
# environment Claude Code runs in.
{{- end}}
{{- if .TimeoutMS}}
# The server's waits outlast Claude Code's 60s limit on an HTTP tool call.
# Add "timeout": {{.TimeoutMS}} to the agora entry that command writes to
# ~/.claude.json, or start Claude Code with MCP_TOOL_TIMEOUT={{.TimeoutMS}}.
{{- end}}

# Or, for one project only, put this in .mcp.json at its root:
{
  "mcpServers": {
    "agora": {
      "type": "http",
      "url": {{.MCPURLString}}{{if .TokenEnv}},
      "headers": { "Authorization": "Bearer ${ {{- .TokenEnv -}} }" }{{end}}{{if .TimeoutMS}},
      "timeout": {{.TimeoutMS}}{{end}}
    }
  }
}

## stop_hook
fields: Command, CommandString (a quoted JSON string)
# Claude Code Stop hook: merge into "hooks" in ~/.claude/settings.json.
# It keeps a session from ending while it is expected in an agora
# conversation, and does nothing unless AGORA_AGENT is set in the session's
# environment (with AGORA_URL and AGORA_NAMESPACE if not the defaults).
{
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command", "command": {{.CommandString}} } ] }
    ]
  }
}
