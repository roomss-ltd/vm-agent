"""Contract driver: the real vm_mcp tools and bin/jarvis against the real control-panel agent API.

Not run on its own: mcp/contract/run.sh <control-panel checkout> starts the panel's handlers on an isolated home
(fake zellij sockets, fake Hermes) and runs this with PANEL_AGENT_URL, PANEL_AGENT_TOKEN_FILE, CONTRACT_HOME and
CONTRACT_SOCK set. Asserts what the client sends and what the panel does with it (records on disk, verified plan
states, refusals). Prints "PANEL GAP:" for each contract breach the panel shows (and then fails), and
"TID=<thread>" for the Go side, which checks the worker's report reached that thread's lead.
"""
import os
import pathlib
import re
import shutil
import subprocess
import sys

import vm_mcp as m

H = pathlib.Path(os.environ["CONTRACT_HOME"])
SOCK = pathlib.Path(os.environ["CONTRACT_SOCK"])
BIN = pathlib.Path(__file__).resolve().parent.parent / "bin"


def refused(fn, *a, want, **k):
    """Call a tool that must be refused: asserts it raises PanelError mentioning `want`, and returns the message."""
    try:
        out = fn(*a, **k)
    except m.PanelError as e:
        assert want in str(e), f"{fn.__name__}{a}{k}: refused, but not with {want!r}: {e}"
        return str(e)
    raise AssertionError(f"{fn.__name__}{a}{k} was accepted: {out}")


GAPS = []


_seen = [0]


def done(name):
    """Mark a scenario as ran to its end: prints ok, or FAILED when it found new panel gaps since the last one."""
    print(("SCENARIO FAILED: " if len(GAPS) > _seen[0] else "SCENARIO ok: ") + name)
    _seen[0] = len(GAPS)


def gap(msg):
    """Record a contract breach by the panel (the run keeps going and fails at the end)."""
    GAPS.append(msg)
    print("PANEL GAP: " + msg)


def owner_feature(sess, plan):
    """A feature the owner started on the Terminals page: its live session and its own record."""
    (SOCK / sess).touch()
    d = H / "jarvis-files" / "features" / sess
    d.mkdir(parents=True)
    (d / "FEATURE.md").write_text("What must be true: the demo file exists.\n")
    (d / "PLAN.md").write_text(plan)
    return d


def thread_id(out):
    """The thread id in thread_open's answer."""
    return re.search(r"opened thread (\S+)", out)[1]


SESS = "Owner-Feature"
PLAN0 = "## M1 Owner's milestone\n- [~] Owner's own step @Builder\n  - kind: build\n- [ ] Second step @Tester\n"
own = owner_feature(SESS, PLAN0)
(SOCK / "scratch").touch()  # a standalone terminal the owner runs alone

# A combined team + plan update is atomic: both land in the newly bound record, or neither changes anything.
def team_of(t):
    """The team session the panel says thread t leads."""
    return next(x["team"] for x in m.panel("GET", "/agent/threads")["threads"] if x["id"] == t)


def record_untouched(t, own, text):
    """True when the owner's PLAN.md still holds `text` and thread t has made no PLAN.md of its own."""
    return (own / "PLAN.md").read_text() == text and not (H / "jarvis-files" / "threads" / t / "PLAN.md").exists()


def check(ok, msg):
    """Record a panel gap with `msg` unless `ok`."""
    if not ok:
        gap(msg)


one = "- [ ] Only step\n"
HELD = "Held-Session"
(SOCK / HELD).touch()
thread_id(m.thread_open("Holder", "Leads Held-Session.", team=HELD))
sess2, sess3 = "Owner-Two", "Owner-Three"
own2, own3 = owner_feature(sess2, one), owner_feature(sess3, one)
t2 = thread_id(m.thread_open("Second manager", "Manage Owner-Two."))
t3 = thread_id(m.thread_open("Third manager", "Manage Owner-Three."))
raw = lambda body: m.panel("POST", "/agent/thread/update", body)  # one request, as the panel's contract defines it
raw({"id": t2, "team": sess2, "plan": "- [~] Only step"})
check(team_of(t2) == sess2 and record_untouched(t2, own2, "- [~] Only step\n"),
      "one update with team + plan: the plan didn't land in the newly bound record alone (updateThread applies plan before team)")
for body, why in (({"id": t3, "team": sess3, "plan": "- [x] Not a step"}, "a refused plan"),
                  ({"id": t3, "team": HELD, "plan": "- [~] Only step"}, "a refused team (another thread leads it)")):
    try:
        raw(body)
        gap(f"one update with team + plan and {why} was accepted")
    except m.PanelError as e:
        check("409" in str(e), f"{why}: {str(e)[:80]!r}, not 409")
    check(team_of(t3) == t3 and record_untouched(t3, own3, one), f"one update with team + plan and {why} changed something (not atomic)")
done("A1 combined team+plan atomic (accept, rejected plan, occupied team)")
# a plan write that fails on disk (the record folder read-only) leaves the thread unbound too
sess4 = "Owner-Four"
own4 = owner_feature(sess4, one)
t4 = thread_id(m.thread_open("Fourth manager", "Manage Owner-Four."))
own4.chmod(0o500)
try:
    raw({"id": t4, "team": sess4, "plan": "- [~] Only step"})
    gap("one update with team + plan whose PLAN.md write fails was accepted")
except m.PanelError as e:
    print(f"write failure answered: {str(e)[:120]}")
finally:
    own4.chmod(0o700)
check(team_of(t4) == t4 and record_untouched(t4, own4, one), "a failed PLAN.md write still bound the team or wrote a record (not atomic)")
done("A2 PLAN.md write failure changes nothing")
# threads.json can't be saved, and only that: its temp name is taken by a directory, so the journal and the PLAN.md write
# succeed and the final save fails (the Go side checks the panel logged a save failure, not an intent one). A refused
# update must leave memory, disk and the record as they were, and no journal behind.
THREADS = pathlib.Path(os.environ["CONTRACT_THREADS"])
sess5 = "Owner-Five"
own5 = owner_feature(sess5, one)
t5 = thread_id(m.thread_open("Fifth manager", "Manage Owner-Five."))
blocker = pathlib.Path(str(THREADS) + ".tmp")
blocker.mkdir()
try:
    raw({"id": t5, "team": sess5, "plan": "- [~] Only step"})
    gap("one update with team + plan whose threads.json save fails was accepted (the binding isn't on disk)")
except m.PanelError as e:
    print(f"save failure answered: {str(e)[:120]}")
finally:
    blocker.rmdir()
check(not pathlib.Path(str(THREADS) + ".plan-txn").exists(), "the journal was left behind after a rolled-back save")
import json as _json
on_disk = next(x.get("team") for x in _json.loads(THREADS.read_text()) if x["id"] == t5)
check(team_of(t5) == t5 and on_disk == t5 and record_untouched(t5, own5, one),
      f"a refused update whose threads.json save failed still changed state: team in memory {team_of(t5)!r}, on disk {on_disk!r}, "
      f"owner PLAN.md {'changed' if (own5 / 'PLAN.md').read_text() != one else 'unchanged'}")
done("A3 threads.json save failure changes nothing")
# the tool sends the same single request: a refused plan leaves the team unbound
refused(m.thread_update, t3, team=sess3, plan="- [x] Not a step", want="409")
check(team_of(t3) == t3, "thread_update(team=, plan=) with a refused plan still bound the team")
done("A4 tool sends one request")
# thread_open(team=) checks no liveness on purpose: the lead may create the session afterwards
ghost = thread_id(m.thread_open("Ghost", "b", team="Not-A-Session"))
check(team_of(ghost) == "Not-A-Session", "thread_open didn't keep the future team it was given")
# ...but binding an existing thread needs a live session, so an update can't point a lead at a name nobody runs
t6 = thread_id(m.thread_open("Sixth manager", "b"))
refused(m.thread_update, t6, team="Future-Session", want="panel said 400")
check(team_of(t6) == t6, "a refused bind to a session that isn't live still changed the team")
done("A6 thread_open may name a future session; update/bind needs a live one")
try:
    m.thread_open("Twin", "b", team=HELD)
    gap(f"thread_open(team=) let a second thread lead {HELD}")
except m.PanelError as e:
    check("409" in str(e), f"thread_open(team=) on a session another thread leads: {str(e)[:70]!r}, not 409")


done("A5 thread_open on occupied team")
# 1. Main opens a manager thread; the manager binds the owner's session by its exact name and moves a box
tid = thread_id(m.thread_open("Owner feature manager", "Manage the owner's feature in the Owner-Feature session."))
refused(m.thread_update, tid, team="owner-feature", want="panel said 400")  # a near name is never guessed
out = m.thread_update(tid, team=SESS, plan="- [x] Owner's own step")
assert f"team {SESS}" in out, out
assert (own / "PLAN.md").read_text() == PLAN0.replace("[~] Owner's", "[x] Owner's"), (own / "PLAN.md").read_text()
assert not (H / "jarvis-files" / "threads" / tid / "PLAN.md").exists(), "binding made a second PLAN.md"
assert "plan 0 of 2 done" in out, out  # a tick without evidence is a claim, not done

done("1 bind by exact name + plan into the owner's record")
# 2. plan= is state-only: a step PLAN.md doesn't have is refused, the file untouched
before = (own / "PLAN.md").read_text()
refused(m.thread_update, tid, plan="- [x] Owner's own step\n- [ ] Deploy to prod", want='"Deploy to prod" (no such step)')
assert (own / "PLAN.md").read_text() == before

done("2 plan= state-only")
# 3. done needs every step verified
refused(m.thread_update, tid, status="done", want="2 of 2 steps aren't verified")

done("3 done gate refuses bare ticks (mechanics only, not false-completion proof: see N1)")
# 4. a worker on the team reports through bin/jarvis: it reaches this thread's lead (checked by the Go side)
env = {**os.environ, "HOME": str(H), "ZELLIJ_SESSION_NAME": SESS, "ZELLIJ_PANE_ID": "3"}
r = subprocess.run([sys.executable, str(BIN / "jarvis"), "--no-wait", "Builder: step one done, evidence in evidence/build.log"],
                   env=env, capture_output=True, text=True, timeout=60)
assert r.returncode == 0 and f"Sent to your lead (thread {tid})" in r.stdout, (r.returncode, r.stdout, r.stderr)

done("4 worker report sent via bin/jarvis")
# 5. workers tick with evidence: one under the record folder, one only in a worktree
wt = H / "worktrees" / tid / "tester"
wt.mkdir(parents=True)
(wt / "test.log").write_text("ok  \tdemo\t1.2s\n")
(own / "evidence").mkdir()
(own / "evidence" / "build.log").write_text("built demo.txt\n")
plan = ("## M1 Owner's milestone\n- [x] Owner's own step @Builder\n  - kind: build\n  - evidence: evidence/build.log\n"
        "  - result: `built demo.txt`\n- [x] Second step @Tester\n  - evidence: {ev}\n  - result: `ok  \tdemo`\n")
(own / "PLAN.md").write_text(plan.format(ev=wt / "test.log"))
def tline(t):
    """The thread_list line of thread t."""
    return next(l for l in m.thread_list(True).splitlines() if l.startswith(t + " "))


check("plan 0 of 2 done" in tline(tid), "file evidence verified a step (worker files stay claimed): " + tline(tid))
refused(m.thread_update, tid, status="done", want="aren't verified")
done("5 file evidence (record or worktree) stays claimed; done refused")

# 6. feature ship removes ~/worktrees/<id>: nothing a worker wrote ever verified, so nothing changes; the owner's
# verification (Go side, G4) is what completes it
shutil.rmtree(H / "worktrees" / tid)
check("plan 0 of 2 done" in tline(tid), "worktree cleanup changed the plan: " + tline(tid))
refused(m.thread_update, tid, status="done", want="aren't verified")
done("6 worktree cleanup changes nothing; done still refused")

# 7. standalone terminal: never enrolled, never given a record
assert "scratch" not in m.thread_list(True)
assert not (H / "jarvis-files" / "features" / "scratch").exists()

done("7 standalone terminal untouched")

# N1, fabricated evidence: an agent writes its own "evidence" with the result it claims and ticks the step, without
# running anything. The bytes match a real log; only independent verification can tell them apart, so the step must
# stay claimed and done must be refused. Scenarios 5-6 exercise the same agent-written-file path: they show the
# gate's mechanics, not proof against false completion.
sess7 = "Owner-Seven"
own7 = owner_feature(sess7, "## Ship\n- [ ] Tests pass @Tester\n")
t7 = thread_id(m.thread_open("Seventh manager", "Manage Owner-Seven."))
m.thread_update(t7, team=sess7)
(own7 / "evidence").mkdir()
(own7 / "evidence" / "fake.log").write_text("ok  \tdemo\t0.1s\n")  # what `echo` would write: no test ran
(own7 / "PLAN.md").write_text("## Ship\n- [x] Tests pass @Tester\n  - evidence: evidence/fake.log\n  - result: `ok  \tdemo`\n")
line7 = next(l for l in m.thread_list().splitlines() if l.startswith(t7 + " "))
check("plan 0 of 1 done" in line7, "fabricated evidence (an agent-written file containing its own claimed result) verifies the step: " + line7)
try:
    m.thread_update(t7, status="done")
    gap("status done accepted on fabricated evidence (false completion)")
except m.PanelError:
    pass
done("N1 fabricated initial evidence never verifies a step or completes the feature")

# N2: status done and a plan moving a step back to todo, in one tool call, must fail as a whole
sess8 = "Owner-Eight"
plan8 = "## Work\n- [x] Check output @Tester\n  - evidence: evidence/output.txt\n  - result: `ok`\n"
own8 = owner_feature(sess8, plan8)
(own8 / "evidence").mkdir()
(own8 / "evidence" / "output.txt").write_text("ok\n")
t8 = thread_id(m.thread_open("Eighth manager", "Manage Owner-Eight."))
m.thread_update(t8, team=sess8)
try:
    m.thread_update(t8, status="done", plan="- [ ] Check output")
    gap("thread_update(status=done, plan=<a todo step>) committed a done thread with a todo step")
except m.PanelError:
    pass
line8 = next(l for l in m.thread_list(True).splitlines() if l.startswith(t8 + " "))
check("[done" not in line8 and (own8 / "PLAN.md").read_text() == plan8, "a refused done+todo update changed state: " + line8)
done("N2 done + todo in one tool call fails whole")
print(f"TID={tid}")
if GAPS:  # every assertion above held; only panel gaps: exit 3 so the Go side still runs its checks
    print(f"contract FAILED: {len(GAPS)} panel gap(s)")
    sys.exit(3)
print("contract ok")
