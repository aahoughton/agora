# Claude Code

## description
Claude Code (claude)

## config_dir
~/.claude

## home_env
CLAUDE_CONFIG_DIR

## skills_dir
@/skills

## hook_contract
claude

## install_mcp
{"file":"~/.claude.json","file_when_home_env":"@/.claude.json","op":"command","path":["mcpServers","smoot"],"run":["claude","mcp","add-json","--scope","user","smoot","{{.ValueJSON}}"],"replace":["claude","mcp","remove","--scope","user","smoot"],"note":"{{if .TokenEnv}}Set {{.TokenEnv}} in the environment Claude Code runs in; it expands the header reference when connecting.{{end}}"}
{
  "type": "http",
  "url": {{.MCPURLString}}{{if .TokenEnv}},
  "headers": {"Authorization": "Bearer ${ {{- .TokenEnv -}} }"}{{end}}{{if .TimeoutMS}},
  "timeout": {{.TimeoutMS}}{{end}}
}

## install_stop_hook
{"file":"@/settings.json","op":"json-append","path":["hooks","Stop"],"identity":"smoot hook stop","note":"Keeps a session in its expected smoot conversations. Does nothing unless SMOOT_AGENT is set in the client's environment; set SMOOT_TOKEN there too for a server with tokens."}
{"hooks": [{"type": "command", "command": {{.CommandString}}}]}

## install_approvals
{"file":"@/settings.json","op":"json-ensure-strings","path":["permissions","allow"]}
["mcp__smoot"]
