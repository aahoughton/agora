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
{"file":"@/config.toml","op":"toml-table","path":["mcp_servers","agora"],"note":"{{if .TokenEnv}}Set {{.TokenEnv}} in the environment Codex runs in for bearer-token authentication.{{end}}"}
[mcp_servers.agora]
url = {{.MCPURLString}}
{{- if .TokenEnv}}
bearer_token_env_var = {{.TokenEnvString}}
{{- end}}
default_tools_approval_mode = "approve"
{{- if .TimeoutSeconds}}
tool_timeout_sec = {{.TimeoutSeconds}}
{{- end}}

## install_stop_hook
{"file":"@/hooks.json","op":"json-append","path":["hooks","Stop"],"identity":"agora hook stop","note":"Keeps a session in its expected agora conversations. Does nothing unless AGORA_AGENT is set in the client's environment; set AGORA_TOKEN there too for a server with tokens.\nTrust this hook with /hooks in the Codex CLI."}
{"hooks": [{"type": "command", "command": {{.CommandString}}}]}
