#!/bin/bash
# Keeps the assistant alive across reboots without systemd or cron: call it from your shell profile (for example
# ~/.zshrc) on every login. It is idempotent, so it is safe to run at any time.
# The assistant normally lives in a tab of the box-servers zellij session (run-agent.sh). When no Hermes gateway
# is running anywhere, this starts run-agent.sh in the background instead, logging to run/agent.log.
cd "$(dirname "$0")" || exit 1
# the gateway runs as `python3 -I -c <launcher> gateway run …`: match its arguments, which only run-agent.sh uses
for p in $(pgrep -f "gateway run --external-supervisor"); do
  [ "$(stat -c %u /proc/$p 2>/dev/null)" = "$(id -u)" ] && exit 0
done
mkdir -p run
nohup ./run-agent.sh >> run/agent.log 2>&1 < /dev/null &
echo "vm-agent started (pid $!), log: $PWD/run/agent.log"
