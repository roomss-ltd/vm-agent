#!/bin/sh
# The vm-agent <-> control-panel contract test: this checkout's real vm_mcp tools and bin/jarvis against a control-panel
# checkout's real agent API. The panel is copied to a temp dir and this folder's *_test.go files are added to it, so
# the tests run inside the panel's own package on an isolated home with fake zellij sockets and a fake Hermes;
# nothing live is touched. Needs go and uv.
#   mcp/contract/run.sh <control-panel checkout>
# Environment: RACE=1 runs under the race detector; TIMEOUT=40m overrides the 20m go test timeout; RUN=<regexp>
# picks other tests (default ^TestVMAgent); LIVE_VM_AGENT_DIR=<deployed vm-agent checkout> also checks that
# checkout's client against the panel (the live-companion test).
set -eu
PANEL=${1:?usage: run.sh <control-panel checkout>}
VM=$(cd "$(dirname "$0")/../.." && pwd)
T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT
echo "vm-agent: $VM $(git -C "$VM" rev-parse --short HEAD)$(git -C "$VM" diff --quiet HEAD || echo ' +uncommitted')"
# the panel's identity: HEAD, its uncommitted diff and untracked files; the same before and after the copy, else retry
ident() {
  cd "$PANEL" && printf 'HEAD %s diff %s untracked %s' "$(git rev-parse HEAD)" "$(git diff HEAD | sha256sum | cut -c1-16)" \
    "$(git ls-files -o --exclude-standard -z | xargs -0r sha256sum | sha256sum | cut -c1-16)"
}
FINAL=no
for try in 1 2 3; do
  A=$(ident)
  rm -rf "$T" && mkdir -p "$T"
  (cd "$PANEL" && tar --exclude=.git --exclude=node_modules -cf - .) | tar -xf - -C "$T"
  B=$(ident)
  [ "$A" = "$B" ] && FINAL=yes && break
done
echo "panel snapshot: $PANEL $B (stable during copy: $FINAL)"
git -C "$PANEL" status --short
cp "$VM"/mcp/contract/*_test.go "$T/"
# -race needs cgo, which may be off in your go environment: RACE=1 turns it on for this command only
[ -n "${RACE:-}" ] && export CGO_ENABLED=1 CC="${RACE_CC:-gcc}"
# the ticket effect seam's tests build only where the panel has the seam (tickets_seam_test.go, tag seam)
TAGS=""; grep -qs "^var featTicketBeforeEffect" "$T"/*.go && TAGS="-tags=seam" && echo "seam tests: on (featTicketBeforeEffect present)"
cd "$T" && VM_AGENT_DIR="$VM" LIVE_VM_AGENT_DIR="${LIVE_VM_AGENT_DIR:-}" go test -count=1 $TAGS ${RACE:+-race} -timeout "${TIMEOUT:-20m}" -run "${RUN:-^TestVMAgent}" -v .
