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
{"file":"@/mcp_config.json","op":"json-set","path":["mcpServers","smoot"],"note":"{{if .TokenEnv}}Set {{.TokenEnv}} in the environment Antigravity runs in; the stdio bridge reads it. Keep smoot on the client's PATH.{{end}}{{if .TokenFile}}Antigravity launches smoot as a stdio bridge that reads the token from {{.TokenFile}}.{{end}}"}
{{if .TokenFile}}{
  "command": {{.SmootPathString}},
  "args": ["mcp", "--url", {{.BaseURLString}}, "--namespace", {{.NamespaceString}}, "--token-file", {{.TokenFileString}}]{{if .TimeoutSeconds}},
  "timeoutSeconds": {{.TimeoutSeconds}}{{end}}
}{{else if .TokenEnv}}{
  "command": "smoot",
  "args": ["mcp", "--url", {{.BaseURLString}}, "--namespace", {{.NamespaceString}}]{{if .TimeoutSeconds}},
  "timeoutSeconds": {{.TimeoutSeconds}}{{end}}
}{{else}}{
  "url": {{.MCPURLString}}{{if .TimeoutSeconds}},
  "timeoutSeconds": {{.TimeoutSeconds}}{{end}}
}{{end}}

## install_stop_hook
{"file":"@/hooks.json","op":"json-set","path":["smoot"],"note":"Keeps a session in its expected smoot conversations. Does nothing without an agent name, from --agent or SMOOT_AGENT in the client's environment. A server with tokens also needs SMOOT_TOKEN there or --token-file."}
{"Stop": [{"type": "command", "command": {{.CommandString}}}]}

## install_approvals
{"file":"~/.gemini/antigravity-cli/settings.json","op":"json-ensure-strings","path":["permissions","allow"]}
[{{range $i, $tool := .ToolNames}}{{if $i}}, {{end}}{{printf "mcp(smoot/%s)" $tool | json}}{{end}}]
