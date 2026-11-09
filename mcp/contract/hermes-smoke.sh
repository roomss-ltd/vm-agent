#!/bin/sh
# Real Hermes, isolated, against a disposable panel: Hermes' own MCP client loads this checkout's vm_mcp and one
# --oneshot turn calls thread_list and thread_update on the test panel (serve_test.go). Nothing live is touched:
#   HERMES_HOME and HOME are fresh temp dirs (no live config, auth.json, memory, skills, sessions, cron or gateway;
#   ~/.claude/.credentials.json doesn't resolve, so no shared OAuth token is read or refreshed);
#   the model key comes ONLY from ANTHROPIC_API_KEY in the caller's environment or a no-echo prompt on the owner's own
#   terminal: inherited by Hermes, never in argv, files or logs; other credential variables are dropped;
#   limits (an estimate, not a cost bound: Hermes has no output-token cap and may make auxiliary calls):
#   agent.max_turns 4, --reasoning low; --usage-file reports the actual tokens, estimated cost and api_calls;
#   toolsets are limited to the vm MCP server (-t mcp-vm), and its tools to thread_list, thread_update and
#   feature_record (tools.include): each only calls the disposable panel's /agent/threads, /agent/thread/update and
#   /agent/feature. Not exposed: vm_status/fleet_status (the panel's snapshot can run host du and gh api against
#   GitHub), pane/terminal tools, render_card/card_templates (local node), thread_open/send (the fake lead Hermes).
# Without --run it sets everything up, starts the disposable panel, prints the exact Hermes command and stops.
#   mcp/contract/hermes-smoke.sh <control-panel checkout> [--run]
# Shared, read-only: the Hermes install (~/.hermes/hermes-agent), uv and its cache, Go's build cache. A real model
# call is billed to that key.
set -eu
PANEL=${1:?usage: hermes-smoke.sh <control-panel checkout> [--run]}
RUN=${2:-}
VM=$(cd "$(dirname "$0")/../.." && pwd)
W=$(mktemp -d)
trap 'touch "$W/srv/stop"; sleep 1; rm -rf "$W"' EXIT
mkdir -p "$W/panel" "$W/srv" "$W/hermes-home" "$W/home"
(cd "$PANEL" && tar --exclude=.git --exclude=node_modules -cf - .) | tar -xf - -C "$W/panel"
cp "$VM"/mcp/contract/*_test.go "$W/panel/"
(cd "$W/panel" && VM_AGENT_SERVE="$W/srv" go test -count=1 -run '^TestVMAgentServe$' -timeout 20m . >"$W/srv/go.log" 2>&1 &)
for _ in $(seq 1 120); do [ -f "$W/srv/panel.env" ] && break; sleep 1; done
[ -f "$W/srv/panel.env" ] || { cat "$W/srv/go.log"; exit 1; }
. "$W/srv/panel.env"
UV=$(command -v uv)
cat >"$W/hermes-home/config.yaml" <<YAML
agent:
  max_turns: 4   # thread_list, thread_update, the answer, one spare: the run can't loop
mcp_servers:
  vm:
    tools:   # only these are registered (exact names; Hermes tools.include is a whitelist); no resource/prompt utilities
      include: [thread_list, thread_update, feature_record]
      resources: false
      prompts: false
    command: $UV
    args: [run, --quiet, --script, $VM/mcp/vm_mcp.py]
    env:
      PANEL_AGENT_URL: "$PANEL_AGENT_URL"
      PANEL_AGENT_TOKEN_FILE: "$PANEL_AGENT_TOKEN_FILE"
      UV_CACHE_DIR: "$(uv cache dir)"
YAML
PROMPT="Use only the vm tools. 1) Call thread_list. 2) Call thread_update with id=$SMOKE_THREAD and plan='- [~] Say hello'. 3) Reply with the two tool outputs verbatim."
set -- HOME="$W/home" HERMES_HOME="$W/hermes-home" UV_CACHE_DIR="$(uv cache dir)" "ANTHROPIC_API_KEY=<inherited, never in argv>" \
  "$HOME/.hermes/hermes-agent/.hermes/bin/hermes" \
  -m claude-opus-5-5 --provider anthropic --reasoning low -t mcp-vm --ignore-rules --usage-file "$W/usage.json" -z "$PROMPT"
echo "disposable panel: $PANEL_AGENT_URL (thread $SMOKE_THREAD, plan $SMOKE_PLAN)"
echo "hermes home: $W/hermes-home   home: $W/home"
echo "command:"; printf ' %s' "$@"; echo
# the MCP server Hermes would start, with the env Hermes would give it, reaching the disposable panel (no Hermes yet)
(cd "$VM/mcp" && env -i PATH="$PATH" HOME="$W/home" UV_CACHE_DIR="$(uv cache dir)" PANEL_AGENT_URL="$PANEL_AGENT_URL" \
  PANEL_AGENT_TOKEN_FILE="$PANEL_AGENT_TOKEN_FILE" "$UV" run --quiet --with "mcp>=2.3,<3" python -c \
  'import vm_mcp as m; print("vm_mcp -> disposable panel:", m.thread_list())')
if [ "$RUN" != "--run" ]; then echo "dry run: not calling Hermes (pass --run, with ANTHROPIC_API_KEY set)"; exit 0; fi
if [ -z "${ANTHROPIC_API_KEY:-}" ]; then # typed by the owner in their own terminal: not echoed, not in argv, files or logs
  [ -t 0 ] || { echo "ANTHROPIC_API_KEY is not set and there is no terminal to ask on"; exit 1; }
  printf 'Anthropic API key (not shown): ' >/dev/tty; stty -echo </dev/tty; IFS= read -r ANTHROPIC_API_KEY </dev/tty; stty echo </dev/tty; echo >/dev/tty
fi
HERMES_BIN="$HOME/.hermes/hermes-agent/.hermes/bin/hermes"
( # the key is inherited, never in any argv (ps); every other credential variable is dropped first, since
  # ANTHROPIC_TOKEN / CLAUDE_CODE_OAUTH_TOKEN would outrank the key and bring in a shared OAuth token
  for v in $(env | sed -n 's/^\(\(ANTHROPIC\|CLAUDE\|HERMES\|OPENAI\|OPENROUTER\|GOOGLE\|GEMINI\|AWS\)[A-Za-z0-9_]*\)=.*/\1/p'); do
    [ "$v" = ANTHROPIC_API_KEY ] || unset "$v"
  done
  export ANTHROPIC_API_KEY HOME="$W/home" HERMES_HOME="$W/hermes-home" UV_CACHE_DIR="$(uv cache dir)"
  exec "$HERMES_BIN" -m claude-opus-5-5 --provider anthropic --reasoning low -t mcp-vm --ignore-rules \
    --usage-file "$W/usage.json" -z "$PROMPT"
) | tee "$W/hermes.out"
unset ANTHROPIC_API_KEY
echo "--- usage (tokens, estimated cost, api_calls)"; cat "$W/usage.json" 2>/dev/null || echo "(no usage report)"
touch "$W/srv/stop"; sleep 2
echo "--- PLAN.md after"; cat "$W/srv/plan-after.md"; cat "$W/srv/thread-after.txt"
grep -q '\[~\] Say hello' "$W/srv/plan-after.md" && echo "SMOKE ok: real Hermes moved the step through vm_mcp" || { echo "SMOKE FAILED"; exit 1; }
