# Pi

## description
Pi coding agent (pi)

## config_dir
~/.pi/agent

## home_env
PI_CODING_AGENT_DIR

## skills_dir
~/.agents/skills

## install_mcp
{"file":"@/mcp.json","op":"json-set","path":["mcpServers","smoot"],"note":"{{if .TokenEnv}}Set {{.TokenEnv}} in the environment Pi runs in; it expands the header reference when connecting.{{end}}"}
{
  "exposure": "direct",
  "url": {{.MCPURLString}}{{if .TokenEnv}},
  "headers": {"Authorization": "Bearer ${ {{- .TokenEnv -}} }"}{{end}}
}

## no_token_file
The Pi profile has no stdio entry for the bridge; use --with-token.

## approvals_note
The Pi profile does not define an approval-file format.
