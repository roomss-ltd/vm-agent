"""Self-check for bin/jarvis against a stand-in Hermes and control panel: python3 bin/test_jarvis.py"""
import json, os, subprocess, sys, tempfile, threading, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

got = {"sessions": [], "chats": [], "typed": [], "threads": []}


class H(BaseHTTPRequestHandler):
    """A stand-in for both Hermes' API server (sessions, chat) and the control panel (threads, pane input)."""

    def log_message(self, *a): pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.path == "/api/sessions":
            got["sessions"].append(body)
            return self.reply(201, {})
        if self.path.endswith("/chat"):
            got["chats"].append((self.path, body))
            if "fail" in body["message"]:
                return self.reply(500, {"error": "boom"})
            time.sleep(3 if "slow" in body["message"] else 0)
            return self.reply(200, {"message": {"role": "assistant", "content": "On it: " + body["message"].split("\n")[1]}})
        if self.path == "/agent/thread/send":
            got["threads"].append(body)
            return self.reply(200, {"ok": True})
        if self.path == "/agent/pane/input":
            got["typed"].append(body)
            return self.reply(200, {"ok": True})
        self.reply(404, {})

    def do_GET(self):
        if self.path == "/agent/threads" and got.get("panel_down"):
            return self.reply(500, {"error": "boom"})
        if self.path == "/agent/threads" and got.get("no_threads"):
            return self.reply(200, {"threads": []})
        if self.path == "/agent/threads":
            return self.reply(200, {"threads": [{"id": "export-v2", "team": "export-v2", "status": "working"}, {"id": "old", "team": "dev-1", "status": "archived"}]})
        self.reply(404, {})

    def reply(self, code, obj):
        """Answer with a JSON body."""
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)


srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
url = f"http://127.0.0.1:{srv.server_port}"
home = tempfile.mkdtemp()
for p, v in ((".config/vm-agent/api-key", "k"), (".config/control-panel/agent-token", "tok-secret-123")):
    os.makedirs(os.path.dirname(os.path.join(home, p)), exist_ok=True)
    open(os.path.join(home, p), "w").write(v)
env = dict(os.environ, HOME=home, JARVIS_URL=url, PANEL_AGENT_URL=url, ZELLIJ_SESSION_NAME="dev-1", ZELLIJ_PANE_ID="23")
jarvis = [sys.executable, os.path.join(os.path.dirname(os.path.abspath(__file__)), "jarvis")]
run = lambda *args: subprocess.run(jarvis + list(args), env=env, capture_output=True, text=True, timeout=30)

r = run("tests pass, PR #9 is up")  # answered while it waits
assert r.returncode == 0 and r.stdout.strip() == "On it: tests pass, PR #9 is up", r
path, body = got["chats"][-1]
assert path == "/api/sessions/agent-dev-1-23/chat", path
assert body["message"].startswith("[From the Claude agent in dev-1:23, working in ") and body["author"]["id"] == "agent:dev-1:23", body
assert got["sessions"][-1] == {"id": "agent-dev-1-23", "title": "Claude agent dev-1:23"}, got["sessions"]

r = run("--wait", "1", "slow question")  # it stops waiting; the answer is typed into the pane later
assert r.returncode == 0 and "still working" in r.stdout, r
for _ in range(60):
    if got["typed"]:
        break
    time.sleep(0.2)
assert got["typed"] == [{"session": "dev-1", "pane": 23, "text": "[From JARVIS] On it: slow question", "enter": True}], got["typed"]

t0 = time.time()
r = run("--no-wait", "slow fyi")  # returns at once; nothing typed back
assert r.returncode == 0 and "Sent to JARVIS" in r.stdout and time.time() - t0 < 2, r
time.sleep(4)
assert len(got["typed"]) == 1 and got["chats"][-1][1]["message"].endswith("slow fyi"), got

r = run("please fail")
assert r.returncode == 1 and "didn't take it (500)" in r.stderr, r
assert not os.listdir(os.path.join(home, ".local/state/vm-agent/agent-replies")), "replies left behind"
# in a thread's team: to its lead, not Main
r = subprocess.run(jarvis + ["tests are green"], env=dict(env, ZELLIJ_SESSION_NAME="export-v2", ZELLIJ_PANE_ID="2"), capture_output=True, text=True, timeout=30)
assert r.returncode == 0 and "Sent to your lead (thread export-v2)" in r.stdout, r
assert got["threads"][-1]["id"] == "export-v2" and got["threads"][-1]["from"] == "agent:export-v2:2" and got["threads"][-1]["text"].endswith("tests are green"), got["threads"]
# the panel can't say which thread leads this session: not sent anywhere (never misrouted to Main), and it says so
got["panel_down"] = True
n_chats, n_threads = len(got["chats"]), len(got["threads"])
r = subprocess.run(jarvis + ["--no-wait", "tests are red"], env=dict(env, ZELLIJ_SESSION_NAME="export-v2", ZELLIJ_PANE_ID="2"), capture_output=True, text=True, timeout=60)
assert r.returncode == 1 and "Not sent" in r.stderr and "500" in r.stderr and "bounded retry" in r.stderr, r
assert len(got["chats"]) == n_chats and len(got["threads"]) == n_threads, "sent somewhere although the lead was unknown"
saved = r.stderr.split("saved in ")[1].split(";")[0]
assert json.load(open(saved)) == {"session": "export-v2", "pane": "2", "cwd": os.getcwd(), "message": "tests are red"}, saved
assert oct(os.stat(saved).st_mode & 0o777) == "0o600" and "FROM THIS PANE (export-v2:2)" in r.stderr and "--resend" in r.stderr, r.stderr
# a resend while the panel still can't answer: the same copy kept, nothing sent
r = subprocess.run(jarvis + ["--resend", saved], env=dict(env, ZELLIJ_SESSION_NAME="export-v2", ZELLIJ_PANE_ID="2"), capture_output=True, text=True, timeout=60)
assert r.returncode == 1 and saved in r.stderr and os.path.exists(saved) and len(os.listdir(os.path.dirname(saved))) == 1, r
assert "tok-secret-123" not in r.stderr + r.stdout and "Bearer" not in r.stderr, r.stderr  # no token in what it prints
got["panel_down"] = False
# from another pane: refused, nothing sent, the copy kept (it never goes out under the caller's identity)
for other in ({"ZELLIJ_SESSION_NAME": "export-v2", "ZELLIJ_PANE_ID": "7"}, {"ZELLIJ_SESSION_NAME": "dev-1", "ZELLIJ_PANE_ID": "2"}):
    r = subprocess.run(jarvis + ["--resend", saved], env=dict(env, **other), capture_output=True, text=True, timeout=60)
    assert r.returncode == 1 and "written in export-v2:2" in r.stderr and os.path.exists(saved), r
    assert len(got["chats"]) == n_chats and len(got["threads"]) == n_threads, "a resend went out from another pane"
# from its own pane once the panel answers: to its lead, as that pane's report; the copy goes
r = subprocess.run(jarvis + ["--resend", saved], env=dict(env, ZELLIJ_SESSION_NAME="export-v2", ZELLIJ_PANE_ID="2"), capture_output=True, text=True, timeout=60)
assert r.returncode == 0 and "Sent to your lead (thread export-v2)" in r.stdout and not os.path.exists(saved), r
assert got["threads"][-1]["from"] == "agent:export-v2:2" and got["threads"][-1]["text"].endswith("tests are red"), got["threads"][-1]
# the panel answering "no threads" is not an error: an unmanaged terminal still reaches Main
got["no_threads"] = True
r = subprocess.run(jarvis + ["--no-wait", "just me"], env=dict(env, ZELLIJ_SESSION_NAME="export-v2", ZELLIJ_PANE_ID="2"), capture_output=True, text=True, timeout=60)
assert r.returncode == 0 and "Sent to JARVIS" in r.stdout and "Not sent" not in r.stderr, r
got["no_threads"] = False
print("ok")
