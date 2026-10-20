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
{"file":"~/.claude.json","file_when_home_env":"@/.claude.json","op":"command","path":["mcpServers","smoot"],"run":["claude","mcp","add-json","--scope","user","smoot","{{.ValueJSON}}"],"replace":["claude","mcp","remove","--scope","user","smoot"],"note":"{{if .TokenEnv}}Set {{.TokenEnv}} in the environment Claude Code runs in; it expands the header reference when connecting.{{end}}{{if .TokenFile}}Claude Code launches smoot as a stdio bridge that reads the token from {{.TokenFile}}.{{end}}"}
{{if .TokenFile}}{
  "type": "stdio",
  "command": {{.SmootPathString}},
  "args": ["mcp", "--url", {{.BaseURLString}}, "--namespace", {{.NamespaceString}}, "--token-file", {{.TokenFileString}}]{{if .TimeoutMS}},
  "timeout": {{.TimeoutMS}}{{end}}
}{{else}}{
  "type": "http",
  "url": {{.MCPURLString}}{{if .TokenEnv}},
  "headers": {"Authorization": "Bearer ${ {{- .TokenEnv -}} }"}{{end}}{{if .TimeoutMS}},
  "timeout": {{.TimeoutMS}}{{end}}
}{{end}}

## install_stop_hook
{"file":"@/settings.json","op":"json-append","path":["hooks","Stop"],"identity":"smoot hook stop","note":"Keeps a session in its expected smoot conversations. Does nothing without an agent name, from --agent or SMOOT_AGENT in the client's environment. A server with tokens also needs SMOOT_TOKEN there or --token-file."}
{"hooks": [{"type": "command", "command": {{.CommandString}}}]}

## install_approvals
{"file":"@/settings.json","op":"json-ensure-strings","path":["permissions","allow"]}
["mcp__smoot"]
