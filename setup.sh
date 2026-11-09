#!/bin/bash
# Install (if missing) and configure the assistant — Hermes, ~/.hermes — from this repo. Idempotent:
# run-agent.sh runs it at every start, so a change here takes effect on the next restart. Run it by hand with
# ./setup.sh; it needs curl, node/npm (for the visual cards) and uv on PATH.
# It never asks for secrets: Telegram and model keys are set in the Hermes dashboard (panel → Assistant
# → Connections) or with `hermes config set NAME value`.
set -euo pipefail
cd "$(dirname "$0")"
HERE=$PWD
unset HTTPS_PROXY https_proxy HTTP_PROXY http_proxy # downloads and config go direct
export PATH="$HOME/.local/bin:$PATH"

if ! command -v hermes >/dev/null; then
  curl -fsSL https://raw.githubusercontent.com/NousResearch/hermes-agent/main/scripts/install.sh |
    bash -s -- --skip-setup --skip-computer-use
fi

# Optional features Hermes would otherwise install on first use, after asking on the console (where nobody
# answers: the turn hangs). stt-whisper: voice notes are transcribed on this VM.
hermes pm install --extra anthropic --extra telegram --extra google --extra mcp --extra cron --extra web \
  --extra ddgs --extra doc-extract --extra vision --extra pty --extra stt-whisper </dev/null >/dev/null
# Hermes' template leaves an empty `max_concurrent_sessions:` that it then warns about every second
hermes config get max_concurrent_sessions >/dev/null 2>&1 && hermes config unset max_concurrent_sessions >/dev/null

ENV=~/.hermes/.env
envget() { sed -n "s/^$1=//p" "$ENV" 2>/dev/null | tail -1 | tr -d "\"'"; }
hc() { [ "$(hermes config get "$1" 2>/dev/null)" = "$2" ] || hermes config set "$1" "$2" >/dev/null; }

# The visual cards' toolkit (message-ui, React → PNG) the vm MCP server's render_card runs
[ -d attachments/node_modules ] || (cd attachments && npm ci --silent --no-audit --no-fund)

# The API server the panel's Assistant view talks to: loopback only, its key shared through a 0600 file.
key=~/.config/vm-agent/api-key
if [ ! -s "$key" ]; then
  mkdir -p -m 700 ~/.config/vm-agent
  (umask 077 && python3 -c 'import secrets; print(secrets.token_hex(32))' > "$key")
fi
[ "$(envget API_SERVER_KEY)" = "$(cat "$key")" ] || hermes config set API_SERVER_KEY "$(cat "$key")" >/dev/null
[ "$(envget API_SERVER_ENABLED)" = true ] || hermes config set API_SERVER_ENABLED true >/dev/null
[ "$(envget API_SERVER_HOST)" = 127.0.0.1 ] || hermes config set API_SERVER_HOST 127.0.0.1 >/dev/null
[ "$(envget API_SERVER_PORT)" = 8642 ] || hermes config set API_SERVER_PORT 8642 >/dev/null

# Telegram: once the owner's user id is allowed, their DM is the home channel (fleet pings, cron).
users=$(envget TELEGRAM_ALLOWED_USERS)
if [ -n "$users" ] && [ -z "$(envget TELEGRAM_HOME_CHANNEL)" ]; then
  hermes config set TELEGRAM_HOME_CHANNEL "${users%%,*}" >/dev/null
fi

# Full access, on purpose: no approval prompts anywhere. Hermes' hardline blocklist
# of catastrophic commands still applies.
hc approvals.mode off
hc approvals.cron_mode approve
hc approvals.unattended_mode approve
hc terminal.cwd "$HOME"

# Voice notes (Telegram): faster-whisper "small" on this VM's CPU (about 5 s for 10 s of speech), let go after
# 30 idle minutes; the prompt teaches it the names it would otherwise mishear.
hc stt.enabled true
hc stt.provider local
hc stt.local.model small
hc stt.local.unload_after_idle_seconds 1800
hc stt.prompt "JARVIS, Hermes, Claude, Codex, zellij, Telegram"

# Who it is, what it knows about the VM, and its VM tools.
ln -sfn "$HERE/hermes/SOUL.md" ~/.hermes/SOUL.md
# A thread's product lead: the control panel sends this playbook with every thread turn (threads.go)
ln -sfn "$HERE/hermes/LEAD.md" ~/.hermes/LEAD.md
hc skills.external_dirs "[$HERE/hermes/skills]"
UV=$(command -v uv)
hc mcp_servers.vm.command "$UV"
hc mcp_servers.vm.args "[run, --quiet, --script, $HERE/mcp/vm_mcp.py]"

# Reply buttons on Telegram: the vm-telegram plugin turns a ```buttons block into buttons under the message.
mkdir -p ~/.hermes/plugins
ln -sfn "$HERE/hermes/plugins/vm-telegram" ~/.hermes/plugins/vm-telegram
hermes config get plugins.enabled 2>/dev/null | grep -q vm-telegram || hermes plugins enable vm-telegram >/dev/null

# Claude agents talk to JARVIS with `jarvis` (bin/jarvis); the jarvis skill tells Claude Code when and how.
mkdir -p ~/.local/bin ~/.claude/skills
ln -sfn "$HERE/bin/jarvis" ~/.local/bin/jarvis
ln -sfn "$HERE/bin/feature" ~/.local/bin/feature # a thread's branch, its team's worktrees, one squashed commit into stage
ln -sfn "$HERE/bin/feature-doc" ~/.local/bin/feature-doc # a feature record, read and written against its revision (the panel's writer)
for h in notify-owner guard-no-merge-hook proof-check fable-advise; do ln -sfn "$HERE/bin/$h" ~/.local/bin/$h; done # agent helpers (the hook still has to be registered in ~/.claude/settings.json, see docs/MECHANISMS.md)
# uv for JARVIS's shell too (`uv run --with pandas …` opens the files the owner sends): its terminal tool
# always has ~/.local/bin on PATH, not always uv's own directory
[ "$(command -v uv)" = ~/.local/bin/uv ] || ln -sfn "$(command -v uv)" ~/.local/bin/uv
ln -sfn "$HERE/claude/skills/jarvis" ~/.claude/skills/jarvis

# Fleet pings: a no-agent cron job delivers the panel's new notifications to the Telegram home chat,
# unwrapped, and mirrors them into that chat's session so a reply has the context.
mkdir -p ~/.hermes/scripts
install -m 644 hermes/scripts/*.py ~/.hermes/scripts/ # Hermes runs cron scripts only from there
hc cron.wrap_response false
hc cron.mirror_delivery true
jobs=$(hermes cron list 2>&1 || true) # captured first: grep -q in a pipe would SIGPIPE it under pipefail
if ! grep -q "Name: *fleet-pings" <<<"$jobs"; then
  hermes cron create "every 1m" --name fleet-pings --script fleet_pings.py --no-agent \
    --deliver telegram --failure-deliver local >/dev/null
fi
echo "vm-agent: Hermes configured"
