# Codex

The profile for the OpenAI Codex CLI. Sections are Go text/templates;
`fields:` lists their data. See internal/profiles for what each key is for.

## description
OpenAI Codex CLI (codex)

## skills_dir
~/.agents/skills

## mcp_config
fields: MCPURL, MCPURLString (a quoted TOML string), TimeoutSeconds, TokenEnv, TokenEnvString (a quoted TOML string)
# Codex: add this to ~/.codex/config.toml.
[mcp_servers.agora]
url = {{.MCPURLString}}
{{- if .TokenEnv}}
# Codex sends the token in {{.TokenEnv}} as a bearer token; set it in the
# environment Codex runs in.
bearer_token_env_var = {{.TokenEnvString}}
{{- end}}
# Without this, codex exec refuses tool calls that need approval (close,
# for one) instead of asking.
default_tools_approval_mode = "approve"
{{- if .TimeoutSeconds}}
# The server's waits outlast Codex's default 60s tool timeout.
tool_timeout_sec = {{.TimeoutSeconds}}
{{- end}}

## stop_hook
fields: Command, CommandString (a quoted JSON string)
# Codex Stop hook: merge into ~/.codex/hooks.json, then trust it with
# /hooks in the Codex CLI (Codex skips hooks it has not been told to trust).
# It keeps a session from ending while it is expected in an agora
# conversation, and does nothing unless AGORA_AGENT is set in the session's
# environment (and AGORA_TOKEN for a server with tokens).
{
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command", "command": {{.CommandString}} } ] }
    ]
  }
}
