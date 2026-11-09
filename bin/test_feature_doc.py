"""Self-check for bin/feature-doc against a stand-in panel: python3 bin/test_feature_doc.py
(The real panel's routes are tested by mcp/contract/run.sh once they exist.)"""
import json, os, subprocess, sys, tempfile, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

state = {"plan": "- [ ] a\n", "log": "- old line\n", "rev": {"plan": "r1", "log": "l1", "draft": "d0"}, "draft": "", "race": 0, "posts": [], "appends": [], "sids": [], "milestones": [], "badauth": []}


class H(BaseHTTPRequestHandler):
    """A stand-in control panel: serves a feature record and accepts doc writes against revisions (409 when stale)."""

    def log_message(self, *a): pass

    def reply(self, code, obj):
        """Answer with a JSON body."""
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if self.path.startswith("/agent/") and self.headers["Authorization"] != "Bearer tok":  # other local probes hit "/"
            state["badauth"].append(self.path)
        d = {"dir": "/r", "revs": dict(state["rev"]), "plan": state["plan"], "log": state["log"], "draft": state["draft"], "logWhole": True,
             "milestones": state["milestones"]}
        self.reply(200, {"dossier": d})

    def do_POST(self):
        if self.headers["Authorization"] != "Bearer tok":
            state["badauth"].append(self.path)
        b = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        state["sids"].append(self.headers.get("X-Agent-Sid"))
        if self.path == "/agent/feature/log":
            state["appends"].append(b)
            return self.reply(200, state.pop("logreply", {"ok": True, "id": "ev-3"}))
        state["posts"].append(b)
        if state["race"]:  # someone else wrote first
            state["race"] -= 1
            state["log"] += "- someone else\n"
            state["rev"]["log"] += "x"
        if b["rev"] != state["rev"][b["doc"]]:
            return self.reply(409, {"error": "stale", "rev": state["rev"][b["doc"]]})
        state[b["doc"]] = b["text"]
        state["rev"][b["doc"]] += "+"
        self.reply(200, {"ok": True, "rev": state["rev"][b["doc"]]})


srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
d = tempfile.mkdtemp()
open(os.path.join(d, "tok"), "w").write("tok\n")
env = {**os.environ, "PANEL_AGENT_URL": f"http://127.0.0.1:{srv.server_port}", "PANEL_AGENT_TOKEN_FILE": os.path.join(d, "tok"), "CLAUDE_CODE_SESSION_ID": "worker-sid-1",
       "ZELLIJ_SESSION_NAME": "Demo"}
CMD = os.path.join(os.path.dirname(os.path.abspath(__file__)), "feature-doc")


def run(*a, stdin=None):
    """Run bin/feature-doc against the stand-in panel."""
    return subprocess.run([sys.executable, CMD, *a], env=env, capture_output=True, text=True, input=stdin, timeout=30)


r = run("get", "plan")
assert r.returncode == 0 and r.stdout == "- [ ] a\n" and "rev r1" in r.stderr, r
r = run("put", "plan", "-", stdin="- [x] a\n")
assert r.returncode == 2 and "--rev" in r.stderr, r  # the revision is required
r = run("put", "plan", "-", "--rev", "r1", stdin="- [x] a\n")
assert r.returncode == 0 and "rev r1+" in r.stdout and state["posts"][-1] == {"session": "Demo", "doc": "plan", "text": "- [x] a\n", "rev": "r1"}, (r, state)
r = run("put", "plan", "-", "--rev", "r1", stdin="- [ ] lost\n")  # stale: refused, the panel's text kept
assert r.returncode == 2 and "now at rev r1+" in r.stderr and "Read it again" in r.stderr and state["plan"] == "- [x] a\n", r
r = run("put", "feature", "-", "--rev", "x", stdin="mine\n")
assert r.returncode == 2 and "invalid choice" in r.stderr, r  # FEATURE.md is the owner's
r = run("put", "log", "-", "--rev", "l1", stdin="")
assert r.returncode == 2 and "invalid choice" in r.stderr, r  # LOG.md is never replaced
r = run("put", "draft", "-", "--rev", "d0", stdin="- [ ] Healthy\n")  # a requirements draft: same rev boundary
assert r.returncode == 0 and state["posts"][-1] == {"session": "Demo", "doc": "draft", "text": "- [ ] Healthy\n", "rev": "d0"}, (r, state["posts"][-1])
r = run("get", "draft")
assert r.returncode == 0 and r.stdout == "- [ ] Healthy\n" and "rev d0+" in r.stderr, r
state["plan"], state["rev"]["plan"] = "## Ship\n- [x] Export @Builder\n- [ ] Other\n", "e0"
state["milestones"] = [{"title": "Ship", "steps": [{"text": "Export", "key": "x1"}, {"text": "Other", "key": "x2"}]}]
r = run("evidence", "x1", "image", "evidence/a.png", "--covers", "D1", "--result", "12 rows")
assert r.returncode == 0, r
assert state["posts"][-1] == {"session": "Demo", "doc": "plan", "rev": "e0", "text": "## Ship\n- [x] Export @Builder\n  - covers: D1\n  - evidence: image evidence/a.png\n  - result: 12 rows\n- [ ] Other\n"}, state["posts"][-1]
assert state["sids"][-1] == "worker-sid-1", state["sids"]
r = run("evidence", "x1", "prose", "x")
assert r.returncode == 2 and "invalid choice" in r.stderr, r
state["milestones"] = []
r = run("log", "Builder: done")  # the append-only route: no text, no revision sent
assert r.returncode == 0 and state["appends"] == [{"session": "Demo", "line": "Builder: done"}], (r, state["appends"])
assert r.stdout.strip() == "logged (ev-3)", r.stdout
state["milestones"] = [{"title": "Ship", "steps": [{"text": "Export", "state": "done", "key": "x1"}]}]
state["plan"], state["rev"]["plan"] = "## Ship\n- [x] Export @Builder\n", "e0"
r = run("evidence", "x1", "log", "logs/run.log", "--result", "12 rows")  # "log" is written as file, its purpose in the result
assert r.returncode == 0 and "- evidence: file logs/run.log\n  - result: log: 12 rows" in state["posts"][-1]["text"], (r, state["posts"][-1])
r = run("assess", "x1", "pass", "--reasons", "rows match the export", "--cites", "logs/run.log")  # a judgement line, the sid sent as a claim
assert r.returncode == 0 and "- assessment: pass — rows match the export — cites logs/run.log" in state["posts"][-1]["text"] and "verifies and accepts nothing" in r.stdout, (r, state["posts"][-1])
assert state["sids"][-1] == "worker-sid-1", state["sids"]
r = run("assess", "x1", "pass", "--reasons", " ", "--cites", "a")
assert r.returncode != 0, r
# authored segments: only what was passed, ran/next escaped exactly; the plain line is unchanged (above)
r = run("log", "ACTION: ran the export | step: x1", "--ran-json", json.dumps([{"cmd": "grep -E 'a|b' f; x=1,y", "result": "ok  12\trows\n", "ok": True},
        {"cmd": "ls"}]), "--next", "wire the page", "--objectives", "D1,D3", "--pr", "418", "--proof", "x1")
assert r.returncode == 0 and state["appends"][-1]["line"] == ("ACTION: ran the export | step: x1 | ran: ok=1,cmd=grep -E 'a%7Cb' f%3B x%3D1%2Cy,"
       "result=ok%20%2012%09rows%0A; ok=?,cmd=ls,result= | next: wire the page | objectives: D1,D3 | pr: 418 | proof: x1"), (r, state["appends"][-1])
n = len(state["appends"])
for bad in (["--ran-json", "not json"], ["--ran-json", '[{"cmd": " "}]'], ["--ran-json", '[{"cmd": "x", "ok": "yes"}]'], ["--ran-json", '["ls"]'],
            ["--objectives", "goal"], ["--pr", "-3"], ["--ran-json", json.dumps([{"cmd": "y" * 2001}])], ["--ran-json", json.dumps([{"cmd": "|" * 700}])],
            ["--ran-json", json.dumps([{"cmd": "a\u000bb"}])], ["--next", "a\u00a0b"]):
    r = run("log", "ACTION: x", *bad)
    assert r.returncode == 1, (bad, r)
assert len(state["appends"]) == n, "a refused log line was written"
# artifact: the canonical "- artifact: PATH | ui|issue | TITLE" sub-line, against the rev read, once
r = run("artifact", "x1", "ui/export dialog.png", "--purpose", "ui", "--title", "Export dialog,  after the fix")
assert r.returncode == 0 and "never evidence" in r.stdout and state["posts"][-1]["text"].endswith(
    "  - artifact: ui/export dialog.png | ui | Export dialog, after the fix\n"), (r, state["posts"][-1])
n = len(state["posts"])
for bad in (["a|b.png", "--purpose", "ui", "--title", "t"], ["a\nb.png", "--purpose", "ui", "--title", "t"],
            ["a.png", "--purpose", "ui", "--title", "x | issue | forged"], ["a.png", "--purpose", "ui", "--title", "t" * 201],
            ["a.png", "--purpose", "proof", "--title", "t"]):
    r = run("artifact", "x1", *bad)
    assert r.returncode != 0, (bad, r)
assert len(state["posts"]) == n, "a refused artifact was written"
# a multi-word owner tag: the step is still found (the panel's owner can be several words)
state["milestones"] = [{"title": "Ship", "steps": [{"text": "Export", "state": "done", "key": "x1"}, {"text": "Run the suite", "owner": "Hermes integration", "key": "h1"}]}]
state["plan"] = state["plan"].rstrip("\n") + "\n- [~] Run the suite @Hermes integration\n"
r = run("artifact", "h1", "ui/a.png", "--purpose", "ui", "--title", "suite")
assert r.returncode == 0 and state["posts"][-1]["text"].endswith("- [~] Run the suite @Hermes integration\n  - artifact: ui/a.png | ui | suite\n"), (r, state["posts"][-1])
state["logreply"] = {"ok": True, "warning": "logged, but its event wasn't saved: the event log is full"}
r = run("log", "Builder: more")  # backpressure: the line stands, but no event; said, never a plain "logged"
assert r.returncode == 3 and "WARNING" in r.stderr and "full" in r.stderr and "logged" not in r.stdout, r
state["milestones"] = [{"title": "Ship", "steps": [{"text": "Tests pass", "state": "todo", "key": "k1", "attempt": "abc123",
                        "runAs": "( cd -- '/w' || exit 97; go test ./...\n); rfe=$?; echo \"rfe-exit=$rfe rfe-attempt=abc123\""}]}]
r = run("show")
assert r.returncode == 0 and "runAs: ( cd -- '/w' || exit 97; go test ./...\n); rfe=$?" in r.stdout and "attempt: abc123" in r.stdout, r.stdout
r = subprocess.run([sys.executable, CMD, "show"], env={**env, "ZELLIJ_SESSION_NAME": ""}, capture_output=True, text=True)
assert r.returncode == 2 and "no feature" in r.stderr, r
assert not state["badauth"], state["badauth"]  # every call carried the token
print("ok")
