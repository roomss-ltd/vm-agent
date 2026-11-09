#!/bin/bash
# The assistant in the foreground, for a tab in the box-servers zellij session: Hermes' gateway
# (Telegram, the API server behind the panel's Assistant view, cron and fleet pings) and its dashboard
# (the panel's Connections portal, /hermes/). Both come back when they exit; Ctrl-C stops them for good.
# Run it from a shell: ./run-agent.sh (it runs setup.sh first, so the configuration is always current).
cd "$(dirname "$0")" || exit 1
unset HTTPS_PROXY https_proxy HTTP_PROXY http_proxy # Telegram and the model APIs go direct
export PATH="$HOME/.local/bin:$PATH"

./setup.sh || echo "vm-agent: setup failed, starting with the config as it is"

# stdin from /dev/null: Hermes never stops to ask a question on this console (nobody is here to answer)
(while true; do
  hermes dashboard --host 127.0.0.1 --port 9119 --no-open --skip-build </dev/null
  sleep 2
done) &
dash=$!
trap 'pkill -P $dash; kill $dash' EXIT

while true; do
  # --external-supervisor: an in-chat /restart or update exits 75 here instead of forking a copy
  hermes gateway run --external-supervisor --replace </dev/null
  code=$?
  [ "$code" = 75 ] && { echo "vm-agent: gateway restarting"; continue; }
  echo "vm-agent: gateway exited ($code): back in 2 s (Ctrl-C to stop)"
  sleep 2
done
