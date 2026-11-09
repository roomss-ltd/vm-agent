"""Self-check for vm_mcp: the text formatters, Claude-readiness detection, and every thread and feature-record tool
against a stand-in control panel (it records the requests the tools make and answers with canned records).
Run: `uv run --with "mcp>=2.3,<3" python mcp/test_vm_mcp.py` (prints "ok").
"""
import os

import vm_mcp as m

HOME = os.path.expanduser("~")

tm = {"agents": 1, "asking": 1, "waiting": 1, "working": 0, "sessions": [{"name": "box-dev", "tabs": [{"id": 1, "name": "auth", "panes": [
    {"id": 3, "kind": "claude", "state": "asking", "detail": "Bash", "git": {"branch": "fix-auth"},
     "agent": {"title": "Fix login", "cwd": HOME + "/repos/x",
               "prompt": {"q": "Run tests?", "opts": [{"n": "1", "label": "Yes"}, {"n": "2", "label": "No"}], "sig": "s"}}},
    {"id": 4, "kind": "shell", "state": "idle"},
]}]}]}
out = m.fmt_fleet(tm)
assert "pane 3 · claude · asking · \"Fix login\" · Bash · branch fix-auth · ~/repos/x" in out, out
assert "QUESTION: Run tests? OPTIONS: 1. Yes | 2. No" in out, out
assert "pane 4 · shell · idle" in out, out

chat = m.fmt_chat([{"k": "user", "text": "hi"}, {"k": "think", "text": "x"}, {"k": "tool", "name": "Edit", "path": "a.go"},
                   {"k": "result", "err": True, "text": "boom"}, {"k": "text", "text": "done"}])
assert chat == "USER: hi\n  [tool Edit a.go]\n  [tool error] boom\nCLAUDE: done", chat

assert m.claude_ready("──\n❯ \n──\n  ⏵⏵ bypass permissions on (shift+tab to cycle)")
assert not m.claude_ready("Starting teamclaude…")
assert m.trim_screen("a  \nb\n\n\n") == "a\nb"
# threads
ts = [{"id": "export-v2", "title": "Export v2", "status": "needs_you", "team": "export-v2", "branch": "feature/export-v2", "note": "Squash into stage?", "live": True},
      {"id": "old", "title": "Old", "status": "archived", "team": "old"}]
out = m.fmt_threads(ts)
assert out == 'export-v2 "Export v2" [needs_you, working now] team export-v2 · branch feature/export-v2 — Squash into stage?', out
assert "old" in m.fmt_threads(ts, True) and m.fmt_threads([]) == "no open threads"
# thread_update(team=, plan=) is one request (the panel applies both or neither); a panel that ignores team is an error
sent = []
m.panel = lambda method, path, body=None, timeout=30: sent.append(body) or {"thread": {"id": "x", "title": "X", "status": "working", "team": body.get("team", "Owner-Feature")}}
assert "team Owner-Feature" in m.thread_update("x", team="Owner-Feature", plan="- [x] a ")
assert sent == [{"id": "x", "plan": "- [x] a", "team": "Owner-Feature"}], sent
sent.clear(); m.thread_update("x", team="Owner-Feature")
assert sent == [{"id": "x", "team": "Owner-Feature"}], sent
m.panel = lambda *a, **k: {"thread": {"id": "x", "title": "X", "status": "working", "team": "x"}}
try:
    m.thread_update("x", team="Owner-Feature"); raise AssertionError("an ignored team passed")
except m.PanelError as e:
    assert "didn't bind" in str(e)
# the record tools: exact routes and bodies; runAs shown verbatim; no owner route reachable from here
calls = []
dos = {"dir": "/r", "revs": {"plan": "p1", "log": "l1"}, "plan": "- [ ] a\n", "milestones": [{"title": "Ship", "steps": [
    {"text": "Tests pass", "state": "claimed", "owner": "Tester", "label": "claimed", "why": "file evidence", "attempt": "abc",
     "runAs": "( cd -- '/w' || exit 97; go test\n); rfe=$?; echo \"rfe-exit=$rfe rfe-attempt=abc\""}]}]}
m.panel = lambda method, path, body=None, timeout=30: calls.append((method, path, body)) or ({"dossier": dos} if method == "GET" else {"ok": True, "rev": "p2"})
out = m.feature_record("Demo", doc="plan")
assert calls[-1] == ("GET", "/agent/feature?session=Demo", None) and "--- plan (rev p1) ---\n- [ ] a" in out, out
assert "runAs: ( cd -- '/w' || exit 97; go test\n); rfe=$?; echo \"rfe-exit=$rfe rfe-attempt=abc\"" in out and "label: claimed" in out, out
assert m.feature_doc_write("Demo", "plan", "- [x] a\n", "p1") == "written; plan is now at rev p2"
assert calls[-1] == ("POST", "/agent/feature/doc", {"session": "Demo", "doc": "plan", "text": "- [x] a\n", "rev": "p1"})
m.feature_log("Demo", "Lead: re-planned")
assert calls[-1] == ("POST", "/agent/feature/log", {"session": "Demo", "line": "Lead: re-planned"})
# the manager's typed tools: same routes, scope guard before any write, no new storage
calls = []
def fake(method, path, body=None, timeout=30):
    """The stand-in panel: answers the record, team, snapshot and chat routes from `cur` / `team_r`, and logs every call."""
    calls.append((method, path, body))
    if path.startswith("/agent/feature?"):
        return {"dossier": cur}
    if path.startswith("/agent/feature/team"):
        if "Other" in path:
            raise m.PanelError("panel said 404: no live session and no record")
        return dict(team_r)
    if path.startswith("/agent/snapshot"):
        return {"sessions": [{"name": "Demo", "tabs": [{"id": 1, "name": "Tester", "panes": [
            {"id": 3, "kind": "claude", "state": "working", "git": {"branch": "f"},
             "agent": {"title": "Run tests", "cwd": "/w", "lastAt": 0, "ctx": {"used": 50, "window": 200}}},
            {"id": 4, "kind": "shell", "state": "idle"}]}]}]}
    if path.startswith("/agent/pane/chat"):
        return {"items": [{"k": "text", "text": "tests green"}]}
    return {"ok": True, "rev": "p2"}
m.panel = fake
writes = lambda: [c for c in calls if c[0] == "POST"]
cur = {"thread": "", "revs": {"plan": "e0"}, "plan": "", "milestones": []}
for f in (lambda: m.create_feature_plan("Demo", "t1", "## A\n- [ ] one\n"), lambda: m.propose_requirement_change("Demo", "t1", "x", "y")):
    try:
        f(); raise AssertionError("an unmanaged session was written")
    except m.PanelError as e:
        assert "standalone terminal stays unmanaged" in str(e), e
cur = {"thread": "t1", "revs": {"plan": "e0"}, "plan": "", "milestones": [{"title": "", "steps": [{"text": "published step", "state": "todo"}]}]}
for f, why in ((lambda: m.create_feature_plan("Demo", "t2", "x"), "not 't2'"), (lambda: m.create_feature_plan("Demo", "t1", "## A\n- [ ] one\n"), "drops steps"),
               (lambda: m.update_feature_plan("Demo", "t1", "x", "e0"), "create_feature_plan")):
    try:
        f(); raise AssertionError("refusal missing: " + why)
    except m.PanelError as e:
        assert why in str(e), e
assert writes() == [], writes()
assert m.create_feature_plan("Demo", "t1", "## A\n- [ ] published step @Tester\n") == "PLAN.md written; rev p2"
assert writes()[-1] == ("POST", "/agent/feature/doc", {"session": "Demo", "doc": "plan", "text": "## A\n- [ ] published step @Tester\n", "rev": "e0", "thread": "t1"})
cur = {"thread": "t1", "revs": {"plan": "p2", "feature": "f1"}, "plan": "## A\n- [ ] published step @Tester\n", "feature": "# Outcomes\n- [ ] Healthy\n- [x] Old\n",
       "milestones": [{"title": "A", "steps": [{"text": "published step", "state": "todo", "owner": "Tester", "key": "k1"}]}]}
try:
    m.create_feature_plan("Demo", "t1", "x"); raise AssertionError("create over an existing plan")
except m.PanelError as e:
    assert "update_feature_plan" in str(e)
m.update_feature_plan("Demo", "t1", "## A\n- [~] published step @Tester\n", "p2")
assert writes()[-1] == ("POST", "/agent/feature/doc", {"session": "Demo", "doc": "plan", "text": "## A\n- [~] published step @Tester\n", "rev": "p2", "thread": "t1"})
assert "--- plan (rev p2) ---" in m.get_feature_plan("Demo")
r = m.get_feature_requirements("Demo")
assert "rev f1" in r and "open outcomes: Healthy" in r, r
m.propose_requirement_change("Demo", "t1", "add SSO", "owner asked")
assert writes()[-1] == ("POST", "/agent/feature/log", {"session": "Demo", "line": "REQUIREMENT PROPOSAL (t1): add SSO · why: owner asked", "thread": "t1"})
m.report_feature_progress("Demo", "blocker", "staging down", step="k1")
assert writes()[-1] == ("POST", "/agent/feature/log", {"session": "Demo", "line": "BLOCKER: staging down | step: k1"})
try:
    m.report_feature_progress("Demo", "verified", "x"); raise AssertionError("a bad kind passed")
except m.PanelError:
    pass
assert not any(c[2] and c[2].get("doc") == "feature" for c in writes()), "FEATURE.md written"
team_r = {"session": "Demo", "thread": "t1", "present": True, "observedAt": 1794096000, "ageSec": 12, "stale": False, "dossierRev": "p2",
          "team": [{"key": "Demo:3", "tab": "Tester", "kind": "claude", "state": "working", "title": "Run tests", "branch": "f", "root": "/w",
                    "ctx": 25, "lastAt": 0, "owns": ["k1"]}]}
t = m.get_feature_team("Demo")
assert t.startswith("observed 2026-11-08 00:00:00 UTC (12 s ago)") and "STALE" not in t and "thread t1 · 1 agents" in t, t
assert "Demo:3 · tab \"Tester\" · claude · working · no activity seen · ctx 25% · branch f · /w" in t and "owns: k1 [todo] published step" in t, t
assert calls[-2][1] == "/agent/feature/team?session=Demo", calls[-2:]
team_r.update(stale=True, ageSec=400); assert "STALE: older than the panel's 90 s" in m.get_feature_team("Demo")
team_r.update(observedAt=0, ageSec=-1); assert m.get_feature_team("Demo").startswith("never sampled") 
team_r.update(present=False, team=[]); t = m.get_feature_team("Demo")
assert "not in that sample: no team visible" in t and "0 agents" in t, t
team_r = dict(team_r, present=True, observedAt=1794096000, ageSec=5, stale=False, team=[
    {"key": "Demo:3", "tab": "Tester", "state": "working", "owns": ["k1"]}, {"key": "Demo:7", "tab": "tester", "state": "waiting", "owns": ["k1"]}])
t = m.get_feature_team("Demo")
assert "AMBIGUOUS: tab name shared with Demo:7" in t and "AMBIGUOUS: tab name shared with Demo:3" in t, t
saved = team_r["team"]
team_r["team"] = [{"key": "Demo:3", "tab": "Tester", "state": "working", "ambiguousTab": True, "owns": []}]
assert "AMBIGUOUS: tab name shared with another pane" in m.get_feature_team("Demo")  # the panel's flag alone is enough
team_r["team"] = saved
t = m.get_team_member("Demo", 7)
assert "Demo:7" in t and "Demo:3 ·" not in t and "CLAUDE: tests green" in t and calls[-1][1] == "/agent/pane/chat?session=Demo&pane=7&n=10", t
for f, why in ((lambda: m.get_team_member("Demo", 9), "no agent Demo:9"), (lambda: m.get_feature_team("Other"), "404")):
    try:
        f(); raise AssertionError("refusal missing: " + why)
    except m.PanelError as e:
        assert why in str(e), e
# writes carry thread= so the panel can refuse a mismatch
assert all(c[2].get("thread") == "t1" for c in writes() if c[1] == "/agent/feature/doc" or "PROPOSAL" in c[2].get("line", "")), writes()
m.report_feature_progress("Demo", "progress", "x", thread="t1"); assert writes()[-1][2]["thread"] == "t1"
# draft requirements: FEATURE.draft.md through doc="draft" under rev and thread; never FEATURE.md; no owner route
cur = {"thread": "t1", "revs": {"plan": "p2", "feature": "f1", "draft": "d0"}, "plan": "x", "feature": "# Outcomes\n- [ ] Healthy\n", "draft": "", "milestones": []}
calls.clear()
assert "no draft" in m.get_feature_requirements("Demo")
assert m.create_draft_requirements("Demo", "t1", "- [ ] Healthy\n- [ ] Rollback\n").startswith("DRAFT written (rev p2)")
assert writes()[-1] == ("POST", "/agent/feature/doc", {"session": "Demo", "doc": "draft", "text": "- [ ] Healthy\n- [ ] Rollback\n", "rev": "d0", "thread": "t1"}), writes()
cur["draft"] = "- [ ] Healthy\n- [ ] Rollback\n"
r = m.get_feature_requirements("Demo")
assert "APPROVED FEATURE.md (rev f1" in r and "DRAFT FEATURE.draft.md (rev d0; not approved" in r and "open outcomes: Healthy\n" in r, r
n = len(writes())
for f, why in ((lambda: m.create_draft_requirements("Demo", "t1", "y"), "update_draft_requirements"), (lambda: m.update_draft_requirements("Demo", "t9", "y", "d0"), "not 't9'")):
    try:
        f(); raise AssertionError("refusal missing: " + why)
    except m.PanelError as e:
        assert why in str(e), e
assert len(writes()) == n
m.update_draft_requirements("Demo", "t1", "- [ ] Healthy\n", "d1")
assert writes()[-1][2] == {"session": "Demo", "doc": "draft", "text": "- [ ] Healthy\n", "rev": "d1", "thread": "t1"}
assert not any(c[2].get("doc") == "feature" for c in writes()), "FEATURE.md written"
# ask_owner: the step becomes [?] with one ask: line (replaced, not duplicated), written against the rev read, then needs_you
cur = {"thread": "t1", "revs": {"plan": "p7"}, "plan": "## Ship\n- [ ] Pick region @Builder\n  - kind: mutation\n- [ ] Other\n",
       "milestones": [{"title": "Ship", "steps": [{"text": "Pick region", "key": "k9", "owner": "Builder"}, {"text": "Other", "key": "k10"}]}]}
calls.clear()
real_fake = m.panel
m.panel = lambda method, path, body=None, timeout=30: (calls.append((method, path, body)) or {"thread": {"id": "t1", "status": "needs_you"}}) if path == "/agent/thread/update" else real_fake(method, path, body, timeout)
r = m.ask_owner("Demo", "t1", "k9", "eu or us?", options="eu | us", why="the DNS change waits on it")
assert r == "asked on step k9 (plan rev p2); only that step waits for the owner", r
assert not any(c[1] == "/agent/thread/update" for c in calls), "a scoped ask set the whole thread needs_you"
doc = [c for c in calls if c[1] == "/agent/feature/doc"][-1][2]
assert doc["rev"] == "p7" and doc["thread"] == "t1", doc
assert doc["text"] == "## Ship\n- [?] Pick region @Builder\n  - ask: eu or us? (options: eu | us) · why: the DNS change waits on it\n  - kind: mutation\n- [ ] Other\n", doc["text"]
cur["plan"] = doc["text"]
m.ask_owner("Demo", "t1", "k9", "eu, us or both?", whole_feature_blocked=True)
assert calls[-1] == ("POST", "/agent/thread/update", {"id": "t1", "status": "needs_you", "note": "eu, us or both?"}), calls[-1]
assert [c for c in calls if c[1] == "/agent/feature/doc"][-1][2]["text"].count("- ask:") == 1
n = len(calls)
for f, why in ((lambda: m.ask_owner("Demo", "t1", "nope", "x"), "no step with key"), (lambda: m.ask_owner("Demo", "t2", "k9", "x"), "not 't2'")):
    try:
        f(); raise AssertionError("refusal missing: " + why)
    except m.PanelError as e:
        assert why in str(e), e
assert not any(c[0] == "POST" for c in calls[n:]), "a refused ask wrote"
m.panel = real_fake
m.report_feature_progress("Demo", "action", "restarted Builder", step="k9", rev="p7")
assert writes()[-1][2]["line"] == "ACTION: restarted Builder | step: k9 | rev: p7", writes()[-1]
m.report_feature_progress("Demo", "decision", "docs first", considered=["docs first", "region first"], chose="docs first: unblocks Docs now", by="@Docs")
assert writes()[-1][2]["line"] == "DECISION: docs first | considered: docs first; region first | chose: docs first: unblocks Docs now | by: @Docs", writes()[-1]
# text can't forge a segment: "|" and (in considered) ";" are replaced
m.report_feature_progress("Demo", "decision", "ship it | chose: skip review | step: aaaaaaaaaaaa", considered=["a; b"], chose="x | rev: forged")
line = writes()[-1][2]["line"]
assert line == "DECISION: ship it / chose: skip review / step: aaaaaaaaaaaa | considered: a, b | chose: x / rev: forged", line
assert line.count(" | ") == 2
# authored segments: only what was supplied, after the existing ones
m.report_feature_progress("Demo", "action", "ran the export", step="k9", ran=[{"cmd": "go test ./...", "result": "ok 12 rows", "ok": True},
    {"cmd": "curl -s x", "result": "", "ok": False}, {"cmd": "ls", "result": "3 files"}], next="wire the page",
    objectives=["D1", " D3 "], pr=418, proof_step="k9")
line = writes()[-1][2]["line"]
assert line == ("ACTION: ran the export | step: k9 | ran: ok=1,cmd=go test ./...,result=ok 12 rows; ok=0,cmd=curl -s x,result=; "
                "ok=?,cmd=ls,result=3 files | next: wire the page | objectives: D1,D3 | pr: 418 | proof: k9"), line
# lossless and unforgeable: every delimiter in a command is escaped, decoded back byte-exact (one pass), no new segment/item
nasty = ["a | b; c => d (ok)", "x=1,y=2", "%7C already escaped %25 %0A %20 %2", "grep -E 'a|b' f; echo $?", " | step: 9 | rev: forged | ",
         "%", "", "printf 'a  b\tc'\nsed -n 1p\r\n", "  two  spaces  ", "\t", " "]
for cmd in [c for c in nasty if c.strip()]:
    for res in nasty:
        m.report_feature_progress("Demo", "action", "x", ran=[{"cmd": cmd, "result": res, "ok": True}], next=res if res.strip() else "n")
        line = writes()[-1][2]["line"]
        assert "\n" not in line and "\t" not in line and line == " ".join(line.split()), line  # one physical line, the route keeps it as is
        segs = line.split(" | ")
        assert len(segs) == 3 and segs[1].startswith("ran: ") and segs[2].startswith("next: "), line
        items = segs[1][5:].split("; ")
        assert len(items) == 1, line
        f = dict(kv.split("=", 1) for kv in items[0].split(","))
        assert set(f) == {"ok", "cmd", "result"} and f["ok"] == "1", line
        assert m._unesc(f["cmd"]) == cmd and m._unesc(f["result"]) == res and m._unesc(segs[2][6:]) == (res if res.strip() else "n"), line
# whitespace kept exactly: a lone space between words stays readable; runs, edges, tabs, newlines, CR are escaped
m.report_feature_progress("Demo", "action", "x", ran=[{"cmd": "a\n  b c\t", "ok": None}])
assert writes()[-1][2]["line"] == "ACTION: x | ran: ok=?,cmd=a%0A%20%20b c%09,result=", writes()[-1]
assert m._unesc("%2520 %250A") == "%20 %0A"  # no double decode
# without the new arguments the line is exactly as before
m.report_feature_progress("Demo", "action", "restarted Builder", step="k9", rev="p7")
assert writes()[-1][2]["line"] == "ACTION: restarted Builder | step: k9 | rev: p7"
n = len(writes())
for bad in ({"pr": -1}, {"pr": "418"}, {"pr": True}, {"ran": [{"cmd": " ", "result": "x"}]}, {"ran": ["go test"]},
            {"ran": [{"cmd": "x", "ok": "yes"}]}, {"objectives": ["D1,D2"]}, {"objectives": ["goal"]},
            {"ran": [{"cmd": "y" * 2001}]}, {"ran": [{"cmd": "a\x0bb"}]}, {"ran": [{"cmd": "a\u00a0b"}]}, {"next": "a\x00b"},
            {"ran": [{"cmd": "|" * 700}]}):  # 700 chars become 2100 escaped: the limit is on the final line
    try:
        m.report_feature_progress("Demo", "action", "x", **bad); raise AssertionError(f"{bad} passed")
    except m.PanelError:
        pass
assert len(writes()) == n, "a refused report wrote"
m.report_feature_progress("Demo", "action", "x", ran=[{"cmd": "y" * 1950}]); assert len(writes()[-1][2]["line"]) <= 2000
# evidence / assessment: one sub-line block at the end of the step's sub-lines, against the rev read, with thread
cur = {"thread": "t1", "revs": {"plan": "p8"}, "plan": "## Ship\n- [x] Export CSV @Builder\n  - kind: build\n- [ ] Other\n",
       "milestones": [{"title": "Ship", "steps": [{"text": "Export CSV", "key": "e1"}, {"text": "Other", "key": "e2"}]}]}
m.add_evidence("Demo", "t1", "e1", "image", "evidence/export.png", covers="D1", result="the CSV opens with 12 rows")
doc = writes()[-1][2]
assert doc["rev"] == "p8" and doc["thread"] == "t1" and doc["text"] == "## Ship\n- [x] Export CSV @Builder\n  - covers: D1\n  - kind: build\n  - evidence: image evidence/export.png\n  - result: the CSV opens with 12 rows\n- [ ] Other\n", doc
cur["plan"] = doc["text"]
m.add_assessment("Demo", "t1", "e1", "pass", "12 rows match the fixture", "evidence/export.png")
assert writes()[-1][2]["text"].endswith("  - result: the CSV opens with 12 rows\n  - assessment: pass — 12 rows match the fixture — cites evidence/export.png\n- [ ] Other\n"), writes()[-1][2]["text"]
n = len(writes())
for f in (lambda: m.add_evidence("Demo", "t1", "e1", "prose", "x"), lambda: m.add_evidence("Demo", "t1", "e1", "file", "a\nb"),
          lambda: m.add_assessment("Demo", "t1", "e1", "verified", "x", "y"), lambda: m.add_assessment("Demo", "t1", "e1", "pass", "", "y"),
          lambda: m.assign_feature_job("Demo", "t1", "e1", ""), lambda: m.assign_feature_job("Demo", "t1", "e1", tab="Docs", relation="adjacent", relation_with="e2"),
          lambda: m.assign_feature_job("Demo", "t1", "e1", tab="Docs", relation="handoff", relation_with="e1")):
    try:
        f(); raise AssertionError("a bad call passed")
    except m.PanelError:
        pass
assert len(writes()) == n, "a refused call wrote"
m.assign_feature_job("Demo", "t1", "e2", tab="Docs", sid="s-123", relation="handoff", relation_with="e1", why="CSV ready")
assert writes()[-1] == ("POST", "/agent/feature/assign", {"session": "Demo", "step": "e2", "tab": "Docs", "thread": "t1", "sid": "s-123",
                        "relation": {"kind": "handoff", "with": "e1", "why": "CSV ready"}}), writes()[-1]
# bearer reads: events page by cursor (limit clamped), open tickets shown in feature_record, nothing written
calls.clear()
m.panel = lambda method, path, body=None, timeout=30: calls.append((method, path, body)) or (
    {"events": [{"id": "ev-7", "seq": 7, "at": "2026-11-08T17:00:00Z", "kind": "assign", "step": "k1", "rev": "p1", "text": "Docs on k1"}]}
    if path.startswith("/agent/feature/events") else {"dossier": {"dir": "/r", "revs": {}}, "tickets": [{"id": "tk1", "kind": "ask", "step": "k9", "title": "eu or us?"}]})
out = m.get_feature_events("Demo", after="ev-6", limit=999)
assert calls[-1][1] == "/agent/feature/events?session=Demo&after=ev-6&limit=200" and "ev-7 11-08 17:00 assign step k1 @p1: Docs on k1" in out and out.endswith("next: after=ev-7"), out
m.panel = lambda method, path, body=None, timeout=30: calls.append((method, path, body)) or {"more": True, "events": [
    {"id": "ev-3", "seq": 3, "at": "2026-11-08T17:20:05Z", "kind": "assign", "step": "k1", "tab": "Docs", "sid": "s9",
     "relation": {"kind": "handoff", "with": "k0", "why": "CSV ready"}}]}
out = m.get_feature_events("Demo")
assert calls[-1][1] == "/agent/feature/events?session=Demo&after=ev-0&limit=50", calls[-1]
assert "ev-3 11-08 17:20 assign step k1 tab Docs sid s9 (claimed) · handoff with k0 (CSV ready)" in out and out.endswith("next: after=ev-3 (more)"), out
m.panel = lambda method, path, body=None, timeout=30: calls.append((method, path, body)) or {"dossier": {"dir": "/r", "revs": {}}, "tickets": [{"id": "tk1", "kind": "ask", "step": "k9", "title": "eu or us?"}]}
out = m.feature_record("Demo")
assert "OPEN OWNER TICKETS" in out and "tk1 ask step k9: eu or us?" in out, out
assert all(c[0] == "GET" for c in calls), calls
# the log route's answer is said: its event id, or the panel's warning (event log full awaiting review), never a plain "logged"
for reply, want in (({"ok": True, "id": "ev-9"}, "logged (ev-9)"), ({"ok": True}, "logged")):
    m.panel = lambda method, path, body=None, timeout=30, reply=reply: reply
    assert m.report_feature_progress("Demo", "action", "x") == want and m.feature_log("Demo", "x") == want, want
m.panel = lambda method, path, body=None, timeout=30: {"ok": True, "warning": "logged, but its event wasn't saved: the event log is full"}
for out in (m.report_feature_progress("Demo", "decision", "x"), m.feature_log("Demo", "x")):
    assert out.startswith("WARNING:") and "full" in out and "no event" in out, out
# kind "log" is written as the panel's canonical file kind, its purpose kept in the result line
calls.clear()
m.panel = lambda method, path, body=None, timeout=30: calls.append((method, path, body)) or (
    {"rev": "p9"} if method == "POST" else {"dossier": {"thread": "t1", "rev": "p8", "revs": {"plan": "p8"}, "plan": "## Ship\n- [ ] Export @Builder\n",
     "milestones": [{"title": "Ship", "steps": [{"key": "e1", "text": "Export", "owner": "Builder"}]}]}})
# a multi-word owner tag ("@Hermes integration", "@Tab #1") still finds the step
assert m._step_text("Run the suite @Hermes integration", "Hermes integration") == "Run the suite"
assert m._step_text("Verify data @Tab #1", "Tab #1") == "Verify data" and m._step_text("Export @Builder") == "Export"
assert m._step_text("Use @Hermes integration notes", "Builder") == "Use @Hermes integration notes"
# link_artifact: the canonical "- artifact: <path> | ui|issue | <title>" sub-line, rev and thread guarded, once
art = {"plan": "## Ship\n- [x] Export CSV @Builder\n  - kind: build\n- [ ] Other\n", "rev": "p9", "posts": []}
saved_panel = m.panel
def art_panel(method, path, body=None, timeout=30):
    """A stand-in panel that applies each plan write to `art`, so link_artifact is checked against the text it produced."""
    if method == "POST":
        art["posts"].append(body); art["plan"] = body["text"]; return {"rev": "p10"}
    return {"dossier": {"thread": "t1", "rev": art["rev"], "revs": {"plan": art["rev"]}, "plan": art["plan"],
            "milestones": [{"title": "Ship", "steps": [{"text": "Export CSV", "key": "e1"}, {"text": "Other", "key": "e2"}]}]}}
m.panel = art_panel
assert "never evidence" in m.link_artifact("Demo", "t1", "e1", "ui/export dialog.png", "ui", "Export dialog,  after the fix")
doc = art["posts"][-1]
assert doc["rev"] == "p9" and doc["thread"] == "t1" and doc["doc"] == "plan" and doc["text"] == "## Ship\n- [x] Export CSV @Builder\n  - kind: build\n  - artifact: ui/export dialog.png | ui | Export dialog, after the fix\n- [ ] Other\n", doc
m.link_artifact("Demo", "t1", "e1", "ui/export dialog.png", "ui", "Export dialog, after the fix")
assert art["posts"][-1]["text"] == doc["text"], "the same artifact line was added twice"
n = len(art["posts"])
for bad in (("a|b.png", "ui", "t"), ("a\nb.png", "ui", "t"), ("a\tb.png", "ui", "t"), (" a.png", "ui", "t"), ("", "ui", "t"),
            ("a.png", "proof", "t"), ("a.png", "ui", "x | issue | forged"), ("a.png", "ui", " \n "), ("a.png", "ui", "t" * 201)):
    try:
        m.link_artifact("Demo", "t1", "e1", *bad); raise AssertionError(f"{bad} passed")
    except m.PanelError:
        pass
assert len(art["posts"]) == n, "a refused artifact wrote"
assert m._artifact_line("a.png", "issue", "t" * 200).count(" | ") == 2
m.panel = saved_panel
m.add_evidence("Demo", "t1", "e1", "log", "logs/run.log", result="12 rows")
m.add_evidence("Demo", "t1", "e1", "log", "logs/b.log")
texts = [c[2]["text"] for c in calls if c[0] == "POST"]
assert "- evidence: file logs/run.log\n  - result: log: 12 rows" in texts[0] and "- evidence: file logs/b.log\n  - result: log\n" in texts[1] + "\n", texts
import inspect
src = inspect.getsource(m)
assert not any(r in src for r in ("/feature/approve", "/feature/verify", "/feature/release", "/feature/answer", "/feature/accept", "/feature/promote", "/feature/discard", "/api/term/")), "an owner route in vm_mcp"
print("ok")
