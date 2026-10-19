#!/usr/bin/env bash
# Run one live agent through a smoot conversation against a scripted host,
# to see whether the agent stays engaged until the conversation concludes.
#
# Usage: scripts/live-pair.sh codex|claude [output-dir]
#
# The host starts an `agreement` conversation, asks QUESTION, pushes back once
# on the agent's first answer, and concludes after the agent concludes,
# agreeing to what the agent agreed to. With MISMATCH=1 the host first
# agrees to something else, so the conclusions differ and the agent must
# conclude again. The script reports each step the agent did or did not
# take, its token use when the CLI reports it, and keeps the transcript in
# the output directory.
#
# This calls a paid model and is not deterministic; it is a development aid,
# not a test. Override PROMPT or QUESTION to try wording changes, and MODEL
# to run a model other than the CLI's default.
#
# Requires: go, jq, curl, and the agent's CLI on PATH.
set -euo pipefail

AGENT_CLI=${1:-}
case "$AGENT_CLI" in
codex | claude) ;;
*)
	echo "usage: $0 codex|claude [output-dir]" >&2
	exit 2
	;;
esac
OUT=${2:-$(mktemp -d -t smoot-live-pair)}
mkdir -p "$OUT"
for cmd in go jq curl "$AGENT_CLI"; do
	command -v "$cmd" >/dev/null || {
		echo "live-pair: $cmd not found" >&2
		exit 2
	}
done

CONV=${CONV:-live-pair}
HOST=live-host
AGENT_NAME=${AGENT_NAME:-$AGENT_CLI-reviewer}
TURN_TIMEOUT=${TURN_TIMEOUT:-5m}
QUESTION=${QUESTION:-"Review question: smoot caps the MCP wait tool at 50 seconds by default, to stay under the 60-second tool timeout several clients use. Is 50s the right default? Give a number and one reason. I will push back once before we agree."}
PUSHBACK=${PUSHBACK:-"Pushback: the server usually runs on localhost or a LAN, so transit takes milliseconds, and every extra renewal costs a tool call. Would you accept 50s if the cap is configurable with --mcp-max-wait? If yes, conclude with your final position; if not, say what would change your mind."}
PROMPT=${PROMPT:-"You have an MCP server named smoot. Join the conversation $CONV as agent $AGENT_NAME and take part until it concludes."}

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN="$OUT/smoot"
(cd "$ROOT" && go build -o "$BIN" ./cmd/smoot)

PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])' 2>/dev/null || echo 18799)
URL="http://127.0.0.1:$PORT"
"$BIN" serve --db "$OUT/smoot.db" --addr "127.0.0.1:$PORT" 2>"$OUT/serve.log" &
SERVER=$!
AGENT_PID=
cleanup() {
	[ -n "$AGENT_PID" ] && kill "$AGENT_PID" 2>/dev/null || true
	kill "$SERVER" 2>/dev/null || true
}
trap cleanup EXIT
for _ in $(seq 1 50); do
	SMOOT_URL=$URL "$BIN" health >/dev/null 2>&1 && break
	sleep 0.1
done

API="$URL/v1/namespaces/default/conversations"
post() { curl -fsS -XPOST "$API/$1" -H 'Content-Type: application/json' -d "$2" >/dev/null; }
curl -fsS -XPOST "$API" -d "$(jq -n --arg c "$CONV" --arg a "$HOST" '{name:$c, agent:$a, end:"agreement", topic:"live pairing check"}')" >/dev/null
post "$CONV/messages" "$(jq -n --arg a "$HOST" --arg b "$QUESTION" '{agent:$a, body:$b}')"

case "$AGENT_CLI" in
codex)
	# The user's own config may give smoot a token variable; this server
	# runs with auth off, so point it at one that is always set.
	(cd "$OUT" && SMOOT_LIVE_PAIR_TOKEN=unused codex exec --approve-for-me --skip-git-repo-check ${MODEL:+-m "$MODEL"} \
		-c "mcp_servers.smoot.url=\"$URL/mcp\"" -c 'mcp_servers.smoot.tool_timeout_sec=60' \
		-c 'mcp_servers.smoot.bearer_token_env_var="SMOOT_LIVE_PAIR_TOKEN"' \
		-o "$OUT/agent-final.txt" "$PROMPT" >"$OUT/agent.log" 2>&1) &
	;;
claude)
	MCP=$(jq -n --arg u "$URL/mcp" '{mcpServers:{smoot:{type:"http", url:$u}}}')
	(cd "$OUT" && claude -p "$PROMPT" ${MODEL:+--model "$MODEL"} --mcp-config "$MCP" --strict-mcp-config \
		--allowedTools 'mcp__smoot__*' --output-format json </dev/null >"$OUT/agent.json" 2>"$OUT/agent.log") &
	;;
esac
AGENT_PID=$!

seen=0
pushed=no
mismatched=no
agent_concluded=no
agent_position=
replied_after_pushback=no
outcome=ok
while :; do
	if ! SMOOT_URL=$URL "$BIN" wait "$CONV" --agent "$HOST" --timeout "$TURN_TIMEOUT" >/dev/null; then
		outcome="agent went quiet for $TURN_TIMEOUT"
		break
	fi
	new=$(curl -fsS -XPOST "$API/$CONV/read" -H 'Content-Type: application/json' -d "$(jq -n --arg a "$HOST" '{agent:$a, limit:500}')")
	from_agent=$(jq --arg a "$AGENT_NAME" '[.messages[] | select(.author == $a and (.kind == "say" or .kind == "conclude"))]' <<<"$new")
	if [ "$pushed" = yes ] && [ "$(jq length <<<"$from_agent")" -gt 0 ]; then
		replied_after_pushback=yes
	fi
	if jq -e 'any(.kind == "conclude")' <<<"$from_agent" >/dev/null; then
		agent_concluded=yes
		agent_position=$(jq -r '[.[] | select(.kind == "conclude")] | last | .body | sub("^\\s+"; "") | split("\n")[0]' <<<"$from_agent")
		if [ "${MISMATCH:-}" = 1 ] && [ "$mismatched" = no ]; then
			# #1 is the start line, which nobody means to agree to.
			post "$CONV/conclude" "$(jq -n --arg a "$HOST" '{agent:$a, body:"AGREED: #1"}')"
			mismatched=yes
			agent_concluded=no
			continue
		fi
		case "$agent_position" in
		AGREED:*) post "$CONV/conclude" "$(jq -n --arg a "$HOST" --arg b "$agent_position" '{agent:$a, body:$b}')" ;;
		*) outcome="agent did not agree: $agent_position" ;;
		esac
		break
	fi
	if [ "$pushed" = no ] && jq -e 'any(.kind == "say")' <<<"$from_agent" >/dev/null; then
		post "$CONV/messages" "$(jq -n --arg a "$HOST" --arg b "$PUSHBACK" '{agent:$a, body:$b}')"
		pushed=yes
	fi
	seen=$((seen + 1))
	[ "$seen" -lt 20 ] || {
		outcome="no conclusion after 20 rounds"
		break
	}
done

# Give the agent time to notice the conclusion and stop on its own.
stopped=no
for _ in $(seq 1 90); do
	if ! kill -0 "$AGENT_PID" 2>/dev/null; then
		stopped=yes
		break
	fi
	sleep 2
done
AGENT_PID_LEFT=$AGENT_PID
[ "$stopped" = yes ] && AGENT_PID=

state=$(curl -fsS "$API/$CONV")
curl -fsS "$API/$CONV/messages?limit=500" | jq -r '.messages[] | "#\(.seq) \(.author) [\(.kind)]\(if .reply_to then " re #\(.reply_to)" else "" end): \(.body)"' >"$OUT/transcript.txt"
agent_state=$(jq -r --arg a "$AGENT_NAME" '.participants[] | select(.agent == $a) | "\(.state), read_seq \(.read_seq)"' <<<"$state")
last_seq=$(jq -r '.last_seq' <<<"$state")

case "$AGENT_CLI" in
codex) tokens=$(grep -A1 '^tokens used' "$OUT/agent.log" | tail -1 || true) ;;
claude) tokens=$(jq -r '.usage | "\(.input_tokens + .cache_creation_input_tokens + .cache_read_input_tokens + .output_tokens) total incl. cache, \(.output_tokens) output"' "$OUT/agent.json" 2>/dev/null || true) ;;
esac

joined=no
grep -q " $AGENT_NAME \[system\]: $AGENT_NAME joined" "$OUT/transcript.txt" && joined=yes
answered=no
grep -q "^#[0-9]* $AGENT_NAME \[say\]" "$OUT/transcript.txt" && answered=yes

{
	echo "agent:                   $AGENT_CLI${MODEL:+ ($MODEL)} as $AGENT_NAME"
	echo "outcome:                 $outcome"
	echo "joined:                  $joined"
	echo "answered:                $answered"
	echo "replied after pushback:  $replied_after_pushback"
	echo "concluded:               $agent_concluded${agent_position:+ ($agent_position)}"
	echo "final state:             ${agent_state:-none} (last_seq $last_seq)"
	echo "stopped on its own:      $stopped${AGENT_PID_LEFT:+ (pid $AGENT_PID_LEFT)}"
	echo "tokens:                  ${tokens:-unknown}"
	echo "output:                  $OUT"
} | tee "$OUT/summary.txt"
