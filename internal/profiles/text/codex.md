# Codex

## description
OpenAI Codex CLI (codex)

## config_dir
~/.codex

## home_env
CODEX_HOME

## skills_dir
~/.agents/skills

## hook_contract
claude

## install_mcp
{"file":"@/config.toml","op":"toml-table","path":["mcp_servers","smoot"],"note":"{{if .TokenEnv}}Set {{.TokenEnv}} in the environment Codex runs in for bearer-token authentication.{{end}}"}
[mcp_servers.smoot]
url = {{.MCPURLString}}
{{- if .TokenEnv}}
bearer_token_env_var = {{.TokenEnvString}}
{{- end}}
default_tools_approval_mode = "approve"
{{- if .TimeoutSeconds}}
tool_timeout_sec = {{.TimeoutSeconds}}
{{- end}}

## install_stop_hook
{"file":"@/hooks.json","op":"json-append","path":["hooks","Stop"],"identity":"smoot hook stop","note":"Keeps a session in its expected smoot conversations. Does nothing unless SMOOT_AGENT is set in the client's environment; set SMOOT_TOKEN there too for a server with tokens.\nTrust this hook with /hooks in the Codex CLI."}
{"hooks": [{"type": "command", "command": {{.CommandString}}, "statusMessage": "smoot: checking conversations"}]}

## approvals_note
Codex approves smoot's tools through default_tools_approval_mode in its MCP entry.
