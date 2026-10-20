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
{"file":"@/config.toml","op":"toml-table","path":["mcp_servers","smoot"],"note":"{{if .TokenEnv}}Set {{.TokenEnv}} in the environment Codex runs in for bearer-token authentication.{{end}}{{if .TokenFile}}Codex launches smoot as a stdio bridge that reads the token from {{.TokenFile}}.{{end}}"}
[mcp_servers.smoot]
{{- if .TokenFile}}
command = {{.SmootPathString}}
args = ["mcp", "--url", {{.BaseURLString}}, "--namespace", {{.NamespaceString}}, "--token-file", {{.TokenFileString}}]
{{- else}}
url = {{.MCPURLString}}
{{- end}}
{{- if .TokenEnv}}
bearer_token_env_var = {{.TokenEnvString}}
{{- end}}
default_tools_approval_mode = "approve"
{{- if .TimeoutSeconds}}
tool_timeout_sec = {{.TimeoutSeconds}}
{{- end}}

## install_stop_hook
{"file":"@/hooks.json","op":"json-append","path":["hooks","Stop"],"identity":"smoot hook stop","note":"Keeps a session in its expected smoot conversations. Does nothing without an agent name, from --agent or SMOOT_AGENT in the client's environment. A server with tokens also needs SMOOT_TOKEN there or --token-file.\nTrust this hook with /hooks in the Codex CLI."}
{"hooks": [{"type": "command", "command": {{.CommandString}}, "statusMessage": "smoot: checking conversations"}]}

## approvals_note
Codex approves smoot's tools through default_tools_approval_mode in its MCP entry.
