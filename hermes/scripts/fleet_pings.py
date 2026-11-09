"""Fleet pings: the control panel's new notifications (agents finished / asking / stuck, CI failures,
security, disk, memory) as one Telegram message. Run every minute by a no-agent Hermes cron job
(setup.sh creates it); whatever this prints is delivered, nothing printed means no message.

Installed into ~/.hermes/scripts/ by setup.sh (Hermes only runs cron scripts from there); the source
of truth is hermes/scripts/ in the vm-agent repo. The last notification id sent is kept in
~/.local/state/vm-agent/pings.json; VM_AGENT_PING_KINDS (comma-separated) picks which kinds are sent.
"""
import json
import os
import sys
import urllib.parse
import urllib.request

PANEL = os.environ.get("PANEL_AGENT_URL", "http://127.0.0.1:7778")
TOKEN_FILE = os.path.expanduser("~/.config/control-panel/agent-token")
STATE = os.path.expanduser("~/.local/state/vm-agent/pings.json")
KINDS = set(os.environ.get("VM_AGENT_PING_KINDS", "done,asking,stalled,ci,sec,disk,mem,thread").split(","))
ICON = {"done": "✅", "asking": "❓", "stalled": "⏳", "ci": "❌", "sec": "🛡", "disk": "💾", "mem": "🧠", "thread": ""}


def panel(path):
    """GET a path of the control panel's loopback agent API (bearer token, never through a proxy); decoded JSON."""
    with open(TOKEN_FILE) as f:
        tok = f.read().strip()
    req = urllib.request.Request(PANEL + path, headers={"Authorization": "Bearer " + tok})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(req, timeout=20) as r:
        return json.loads(r.read())


def question(session, pane):
    """The options of the dialog an asking agent shows, so the owner can answer from the ping."""
    try:
        p = panel("/agent/pane/screen?" + urllib.parse.urlencode({"session": session, "pane": pane})).get("prompt")
    except Exception:
        return ""
    if not p:
        return ""
    opts = "\n".join(f"  {o.get('n') or '•'}. {o['label']}" for o in p.get("opts", []))
    return f"{p.get('q', '')}\n{opts}"


def teams():
    """The zellij sessions threads' teams work in: their news goes to their lead (the panel does that), not here."""
    try:
        return {t["team"] for t in panel("/agent/threads").get("threads") or [] if t.get("status") != "archived"}
    except Exception:
        return set()


def render(note):
    """One notification as Telegram text: an icon and title, a [tag] a reply can be routed by, and a short body."""
    line = f"{ICON.get(note['kind'], '•')} {note['title']}".strip()
    go = note.get("go", "")
    if go.startswith("thread:"):  # a lead that needs the owner: the tag on the first line routes a reply back
        line += f" [{go}]"
        body = note.get("body", "")
    elif go.startswith("pane:"):
        _, session, pane = go.split(":", 2)
        line += f" [{session}:{pane}]"
        body = question(session, pane) if note["kind"] == "asking" else ""
        body = body or note.get("body", "")
    else:
        body = note.get("body", "")
    return line + (f"\n{body[:600]}" if body else "")


def main():
    """Print the notifications that are new since the last run (nothing printed means nothing is delivered)."""
    try:
        notes = panel("/agent/notes").get("notes") or []  # newest first
    except Exception as e:
        print(f"fleet pings: control panel unreachable ({e})", file=sys.stderr)
        return
    top = max((n["id"] for n in notes), default=0)
    try:
        with open(STATE) as f:
            last = json.load(f)["last"]
    except (OSError, ValueError, KeyError):
        last = None
    if last is None or last > top:  # first run, or the panel's notes were reset: start from now
        last = top
    team = teams()
    new = [n for n in reversed(notes) if n["id"] > last and n["kind"] in KINDS
           and not (n.get("go", "").startswith("pane:") and n["go"].split(":")[1] in team)]
    os.makedirs(os.path.dirname(STATE), exist_ok=True)
    with open(STATE + ".tmp", "w") as f:
        json.dump({"last": top}, f)
    os.replace(STATE + ".tmp", STATE)
    if new:
        print("Fleet\n" + "\n\n".join(render(n) for n in new[-10:]) + "\n\nReply to this message to answer or instruct an agent.")


if __name__ == "__main__":
    main()
