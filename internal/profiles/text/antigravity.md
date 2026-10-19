# Antigravity

## description
Antigravity CLI (agy)

## config_dir
~/.gemini/config

## skills_dir
@/skills

## hook_contract
antigravity

## install_mcp
{"file":"@/mcp_config.json","op":"json-set","path":["mcpServers","agora"],"note":"{{if .TokenEnv}}Set {{.TokenEnv}} in the environment Antigravity runs in; the stdio bridge reads it. Keep agora on the client's PATH.{{end}}"}
{{if .TokenEnv}}{
  "command": "agora",
  "args": ["mcp", "--url", {{.BaseURLString}}, "--namespace", {{.NamespaceString}}]{{if .TimeoutSeconds}},
  "timeoutSeconds": {{.TimeoutSeconds}}{{end}}
}{{else}}{
  "url": {{.MCPURLString}}{{if .TimeoutSeconds}},
  "timeoutSeconds": {{.TimeoutSeconds}}{{end}}
}{{end}}

## install_stop_hook
{"file":"@/hooks.json","op":"json-set","path":["agora"],"note":"Keeps a session in its expected agora conversations. Does nothing unless AGORA_AGENT is set in the client's environment; set AGORA_TOKEN there too for a server with tokens."}
{"Stop": [{"type": "command", "command": {{.CommandString}}}]}

## install_approvals
{"file":"~/.gemini/antigravity-cli/settings.json","op":"json-ensure-strings","path":["permissions","allow"]}
[{{range $i, $tool := .ToolNames}}{{if $i}}, {{end}}{{printf "mcp(agora/%s)" $tool | json}}{{end}}]
