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
{"file":"@/mcp_config.json","op":"json-set","path":["mcpServers","smoot"],"note":"{{if .TokenEnv}}Set {{.TokenEnv}} in the environment Antigravity runs in; the stdio bridge reads it. Keep smoot on the client's PATH.{{end}}"}
{{if .TokenEnv}}{
  "command": "smoot",
  "args": ["mcp", "--url", {{.BaseURLString}}, "--namespace", {{.NamespaceString}}]{{if .TimeoutSeconds}},
  "timeoutSeconds": {{.TimeoutSeconds}}{{end}}
}{{else}}{
  "url": {{.MCPURLString}}{{if .TimeoutSeconds}},
  "timeoutSeconds": {{.TimeoutSeconds}}{{end}}
}{{end}}

## install_stop_hook
{"file":"@/hooks.json","op":"json-set","path":["smoot"],"note":"Keeps a session in its expected smoot conversations. Does nothing unless SMOOT_AGENT is set in the client's environment; set SMOOT_TOKEN there too for a server with tokens."}
{"Stop": [{"type": "command", "command": {{.CommandString}}}]}

## install_approvals
{"file":"~/.gemini/antigravity-cli/settings.json","op":"json-ensure-strings","path":["permissions","allow"]}
[{{range $i, $tool := .ToolNames}}{{if $i}}, {{end}}{{printf "mcp(smoot/%s)" $tool | json}}{{end}}]
