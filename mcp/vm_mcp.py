#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["mcp>=2.3,<3"]
# ///
"""VM tools for the assistant (MCP server "vm"): the zellij fleet (Claude agents and shells), the VM's health and
CI, threads and feature records, and visual cards.

Everything goes through the control panel's loopback agent API (agent.go in the control-panel repo), which
already knows the panes, their agents, transcripts and on-screen questions. New tools go here: one
@mcp.tool() function each; the shell covers the rest. Hermes starts this file with `uv run --script`
(setup.sh registers it), and the PANEL_AGENT_URL / PANEL_AGENT_TOKEN_FILE environment variables point it at
a different panel (the contract tests do that). Self-check: test_vm_mcp.py next to this file.
"""
import json
import os
import re
import shlex
import shutil
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

from mcp.server.mcpserver import MCPServer

PANEL = os.environ.get("PANEL_AGENT_URL", "http://127.0.0.1:7778")
TOKEN_FILE = os.path.expanduser(os.environ.get("PANEL_AGENT_TOKEN_FILE", "~/.config/control-panel/agent-token"))
HOME = os.path.expanduser("~").rstrip("/")

mcp = MCPServer("vm", instructions="Tools for this VM: the zellij fleet of Claude agents and shells, VM health, CI, panel events.")


class PanelError(Exception):
    """The control panel refused a request or could not be reached; the message says why, for the model to read."""


def panel(method, path, body=None, timeout=30):
    """Call the panel's agent API (bearer token from TOKEN_FILE, JSON in and out); raises PanelError on any failure."""
    with open(TOKEN_FILE) as f:
        token = f.read().strip()
    req = urllib.request.Request(PANEL + path, method=method, headers={"Authorization": "Bearer " + token})
    if body is not None:
        req.data = json.dumps(body).encode()
        req.add_header("Content-Type", "application/json")
    # the panel is on loopback: never through the agent sessions' HTTPS proxy
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req, timeout=timeout) as r:
            return json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        try:
            msg = json.loads(e.read()).get("error", "")
        except Exception:
            msg = ""
        raise PanelError(f"panel said {e.code}: {msg or e.reason}") from None
    except urllib.error.URLError as e:
        raise PanelError(f"control panel not reachable at {PANEL} ({e.reason}); is it running?") from None


def q(**kw):
    """A URL query string from keyword arguments."""
    return "?" + urllib.parse.urlencode(kw)


# ---- formatting: compact text, cheap on tokens ----

def tilde(path):
    """path with the home directory written as ~."""
    return "~" + path[len(HOME):] if path == HOME or path.startswith(HOME + "/") else path


def fmt_prompt(p):
    """The question a pane is blocked on and its options, on one line ("" when there is none)."""
    if not p:
        return ""
    opts = " | ".join(f"{o.get('n') or '-'}. {o['label']}" for o in p.get("opts", []))
    return f"QUESTION: {p.get('q', '')} OPTIONS: {opts}"


def fmt_fleet(tm):
    """The panel's terminals snapshot as indented text: sessions, tabs and panes with their agent state."""
    out = [f"agents {tm.get('agents', 0)}, asking {tm.get('asking', 0)}, waiting {tm.get('waiting', 0)}, working {tm.get('working', 0)}"]
    for s in tm.get("sessions") or []:
        out.append(f"session {s['name']}" + (f" ERROR {s['err']}" if s.get("err") else ""))
        for t in s.get("tabs") or []:
            out.append(f"  tab {t['id']} \"{t['name']}\"")
            for p in t.get("panes") or []:
                a = p.get("agent") or {}
                cwd = a.get("cwd") or ""
                git = p.get("git") or {}
                bits = [f"pane {p['id']}", p.get("kind", ""), p.get("state", "")]
                if a.get("title"):
                    bits.append(f"\"{a['title']}\"")
                if p.get("detail") and p["detail"] != a.get("title"):
                    bits.append(p["detail"][:160])
                if git.get("branch"):
                    bits.append(f"branch {git['branch']}")
                if cwd:
                    bits.append(tilde(cwd))
                if a.get("subagents"):
                    bits.append(f"{len(a['subagents'])} subagents")
                line = "    " + " · ".join(b for b in bits if b)
                if a.get("prompt"):
                    line += "\n      " + fmt_prompt(a["prompt"])
                out.append(line)
    return "\n".join(out)


def fmt_chat(items):
    """A Claude conversation (the panel's transcript items) as short USER / CLAUDE / tool lines."""
    out = []
    for it in items:
        k = it.get("k")
        if k == "user":
            out.append("USER: " + (it.get("text") or "")[:2000])
        elif k == "text":
            out.append("CLAUDE: " + (it.get("text") or "")[:3000])
        elif k == "tool":
            out.append(f"  [tool {it.get('name', '')}{' ' + it['path'] if it.get('path') else ''}]")
        elif k == "result" and it.get("err"):
            out.append("  [tool error] " + (it.get("text") or "")[:300])
        elif k == "note":
            out.append("  [note] " + (it.get("text") or "")[:300])
    return "\n".join(out)


def trim_screen(s):
    """The bottom 80 non-empty lines of a pane's screen, trailing spaces removed."""
    lines = [ln.rstrip() for ln in s.splitlines()]
    while lines and not lines[-1]:
        lines.pop()
    return "\n".join(lines[-80:])


READY = re.compile(r"bypass permissions|\? for shortcuts|^❯\s*$", re.M)


def claude_ready(screen):
    """True when the screen shows Claude Code's empty input box, ready to take a first prompt."""
    return bool(READY.search(screen))


# ---- tools ----

@mcp.tool()
def fleet_status() -> str:
    """Every zellij session -> tab -> pane on the VM with what runs there: Claude agents (state: asking =
    blocked on a question, waiting = finished its turn and waits for the owner, working, stalled), shells
    and commands. Shows each agent's title, cwd, branch and any question it is blocked on with its options.
    Use the session name and pane id with the other pane_* tools."""
    return fmt_fleet(panel("GET", "/agent/snapshot" + q(view="term")))


@mcp.tool()
def pane_chat(session: str, pane: int, last: int = 20) -> str:
    """The last `last` messages (max 300) of the Claude conversation running in a pane: what the owner
    asked, what Claude answered, which tools it used. Use it to see what an agent did or is asking."""
    r = panel("GET", "/agent/pane/chat" + q(session=session, pane=pane, n=last))
    return f"{r.get('title') or ''} [{r.get('state')}]\n" + fmt_chat(r.get("items") or [])


@mcp.tool()
def pane_screen(session: str, pane: int) -> str:
    """What a pane shows right now (bottom 80 lines), plus the question on it if a TUI is blocked on one.
    Works for any pane: shells, servers, logs, Claude."""
    r = panel("GET", "/agent/pane/screen" + q(session=session, pane=pane))
    p = fmt_prompt(r.get("prompt"))
    return trim_screen(r.get("screen", "")) + ("\n\n" + p if p else "")


@mcp.tool()
def pane_files(session: str, pane: int) -> str:
    """The files a pane's Claude session wrote, edited or handed over (its artifacts: designs, mockups, reports,
    code), newest first, with whether each still exists. To show the owner one in the chat, post an ```artifact
    block with its path (see your instructions)."""
    r = panel("GET", "/agent/pane/files" + q(session=session, pane=pane))
    fs = sorted(r.get("files") or [], key=lambda f: -(f.get("last") or 0))
    if not fs:
        return "no files from this session"
    def row(f):
        what = "new" if f.get("created") else ("named" if f.get("mentioned") else f"{f.get('edits')} edits")
        return f"{f['path']}  ({what}{'' if f.get('exists') else ', gone'})"
    return "\n".join(row(f) for f in fs[:60])


@mcp.tool()
def pane_send(session: str, pane: int, text: str, enter: bool = True) -> str:
    """Type text into a pane (pasted, then Enter unless enter=false) without focusing it, so its session
    is not disturbed. For a Claude agent this sends it a message (queued if it is busy); for a shell it
    runs a command. Up to 100 KB."""
    panel("POST", "/agent/pane/input", {"session": session, "pane": pane, "text": text, "enter": enter})
    return f"sent to {session}:{pane}"


@mcp.tool()
def pane_answer(session: str, pane: int, choice: str) -> str:
    """Answer the question a pane is blocked on (a Claude permission or question dialog) by the label of
    one of its options, e.g. "Yes" or "No, and tell Claude what to do differently". Read the options with
    fleet_status or pane_screen first."""
    r = panel("GET", "/agent/pane/screen" + q(session=session, pane=pane))
    p = r.get("prompt")
    if not p:
        return "no question on that pane's screen right now"
    labels = [o["label"] for o in p.get("opts", [])]
    pick = next((l for l in labels if l == choice), None) or next((l for l in labels if l.lower().startswith(choice.lower())), None)
    if not pick:
        return f"no option {choice!r}; options are: {labels}"
    panel("POST", "/agent/pane/input", {"session": session, "pane": pane, "choose": pick, "sig": p["sig"]})
    return f"answered {pick!r} to: {p.get('q', '')}"


@mcp.tool()
def pane_keys(session: str, pane: int, keys: list[str]) -> str:
    """Press keys in a pane. Allowed: Enter, Esc, Tab, Shift Tab, Up, Down, Left, Right, Backspace,
    Ctrl c, PageUp, PageDown, y, n, 0-9. E.g. ["Esc"] interrupts Claude, ["Ctrl c"] stops a command."""
    panel("POST", "/agent/pane/input", {"session": session, "pane": pane, "keys": keys})
    return f"pressed {keys} in {session}:{pane}"


@mcp.tool()
def open_terminal(what: str, name: str, cwd: str = "~", session: str = "", tab: int = -1,
                  run: str = "shell", prompt: str = "", command: str = "") -> str:
    """Open a zellij session, tab or pane and optionally start something in it.
    what: "session" (name = new session name), "tab" (in `session`, name = tab name) or "pane" (in `session`
    and `tab` id, name = pane name). cwd: the directory (absolute or ~/...).
    run="claude" starts a Claude Code agent (started by the panel, permissions bypassed); with `prompt`, it
    waits until Claude is ready (accepting the folder-trust dialog) and sends the prompt as its first task.
    run="shell" with `command` types and runs that command in the new shell.
    Returns the session and pane id to use with the pane_* tools."""
    if run not in ("shell", "claude"):
        return "run must be shell or claude"
    body = {"what": what, "session": session, "name": name, "cwd": cwd, "run": "claude" if run == "claude" else "", "tab": tab}
    r = panel("POST", "/agent/term/new", body)
    s, p = r["session"], r["pane"]
    if run == "shell" and command:
        time.sleep(0.5)  # the shell's prompt
        # some sessions' layouts start new shells in ~ whatever the cwd: go there first
        line = f"cd {shlex.quote(os.path.expanduser(cwd))} && {command}"
        panel("POST", "/agent/pane/input", {"session": s, "pane": p, "text": line, "enter": True})
        return f"opened {what} {name!r}: session {s} pane {p}, ran: {command}"
    if run == "claude" and prompt:
        deadline, trusted = time.time() + 90, False
        while time.time() < deadline:
            time.sleep(1.5)
            sc = panel("GET", "/agent/pane/screen" + q(session=s, pane=p))
            pr = sc.get("prompt")
            if pr and not trusted and re.search(r"trust", (pr.get("q", "") + " ".join(pr.get("ctx") or [])), re.I):
                yes = next((o["label"] for o in pr["opts"] if o["label"].lower().startswith("yes")), None)
                if yes:
                    panel("POST", "/agent/pane/input", {"session": s, "pane": p, "choose": yes, "sig": pr["sig"]})
                    trusted = True
                    continue
            if not pr and claude_ready(sc.get("screen", "")):
                time.sleep(1)  # the input box takes keys a moment after it draws
                panel("POST", "/agent/pane/input", {"session": s, "pane": p, "text": prompt, "enter": True})
                return f"opened {what} {name!r}: session {s} pane {p}; Claude started and got the prompt"
        return f"opened {what} {name!r}: session {s} pane {p}; Claude did not look ready within 90 s, prompt NOT sent (check pane_screen)"
    return f"opened {what} {name!r}: session {s} pane {p}" + (" running Claude" if run == "claude" else "")


@mcp.tool()
def close_terminal(what: str, session: str, tab: int = -1, pane: int = -1) -> str:
    """Close a zellij pane, tab or whole session ("pane" needs pane id, "tab" needs tab id). Whatever runs
    there is killed: confirm with the owner before closing a working agent."""
    panel("POST", "/agent/term/close", {"what": what, "session": session, "tab": tab, "pane": pane})
    return f"closed {what} in {session}"


@mcp.tool()
def vm_status(view: str = "res") -> str:
    """The VM's state as the control panel sees it. view: "res" (CPU, memory, disk, network, top process
    groups), "ci" (GitHub Actions runners, recent jobs, failures) or "sec" (security checklist: open
    ports, ssh keys, secrets' permissions, tunnel). JSON."""
    if view not in ("res", "ci", "sec"):
        return "view must be res, ci or sec"
    d = panel("GET", "/agent/snapshot" + q(view=view))
    if view == "res":
        d = d.get("res", d)
        d.pop("cores", None)
        d["groups"] = [{k: v for k, v in g.items() if k != "procs"} for g in (d.get("groups") or [])[:15]]
    if view == "ci":
        d["recent"] = (d.get("recent") or [])[:20]
    return json.dumps(d, separators=(",", ":"))[:20000]


@mcp.tool()
def recent_events(limit: int = 20) -> str:
    """The panel's latest notifications, newest first: agents that finished or need an answer or look
    stuck, CI failures, security checks gone bad, disk or memory filling. Each has a `go` (pane:<session>:<id>)."""
    notes = (panel("GET", "/agent/notes").get("notes") or [])[:max(1, min(limit, 200))]
    return "\n".join(
        f"#{n['id']} {time.strftime('%m-%d %H:%M', time.localtime(n['t']))} [{n['kind']}/{n['level']}] {n['title']}"
        + (f" — {n['body']}" if n.get("body") else "") + f" ({n['go']})"
        for n in notes
    ) or "no events"


# ---- threads: Main JARVIS is the owner's executive assistant; a thread is a product lead JARVIS for one feature
# (threads.go in the control panel). Main opens and messages them; a lead keeps its own record. ----

def fmt_threads(ts, all_=False):
    """One line per thread (id, title, status, team session, branch, repo, plan progress, the lead's note)."""
    out = []
    for t in ts:
        if t["status"] == "archived" and not all_:
            continue
        line = f"{t['id']} \"{t['title']}\" [{t['status']}{', working now' if t.get('live') else ''}] team {t['team']}"
        if t.get("branch"):
            line += f" · branch {t['branch']}"
        if t.get("repo"):
            line += f" · {t['repo']}"
        if t.get("plan"):
            line += f" · plan {sum(s['state'] == 'done' for s in t['plan'])} of {len(t['plan'])} done"
        if t.get("note"):
            line += f" — {t['note']}"
        out.append(line)
    return "\n".join(out) or "no open threads"


@mcp.tool()
def thread_list(archived: bool = False) -> str:
    """The threads: each one a product lead JARVIS (its own conversation) running one feature with a team of
    Claude or Codex agents in the zellij session named after it. Shows id, title, status (planning, working,
    needs_you, done, archived), team session, branch, repo and the lead's one-line note."""
    return fmt_threads(panel("GET", "/agent/threads").get("threads") or [], archived)


@mcp.tool()
def thread_open(title: str, brief: str, repo: str = "", team: str = "") -> str:
    """(Main JARVIS) Open a thread: a fresh JARVIS, with your rules and memory, that leads ONE feature or piece of
    work end to end (it plans it, runs a team of agents in a zellij session, ships it on its own branch). Use it
    when the owner asks for work bigger than a few replies, or says to open a thread. title: a few words.
    brief: everything the lead needs, self-contained (what the owner wants and why, constraints, where the code
    is, what done means); it can't see your chat. repo: the checkout, if known. team: the exact name of the zellij
    session it leads, live or one its lead will start (unlike thread_update, which binds only a live one), to adopt (a takeover the owner approved, or a feature the owner started on the Terminals page: its agents
    become the lead's team, its record stays where it is). Leave it empty for a standalone terminal: those stay the
    owner's. Returns the thread id; the
    owner sees it in the panel at once."""
    t = panel("POST", "/agent/thread/open", {"title": title, "brief": brief, "repo": repo, "team": team})["thread"]
    return f"opened thread {t['id']} \"{t['title']}\": its lead is starting on the brief (team session {t['team']})"


@mcp.tool()
def thread_send(id: str, text: str, from_owner: bool = False) -> str:
    """(Main JARVIS) A message to a thread's lead: it arrives as a new turn in its conversation. from_owner=true
    when you are passing on the owner's own words (e.g. their Telegram reply to a "🧵 … [thread:<id>]" ping):
    send their text word for word."""
    r = panel("POST", "/agent/thread/send", {"id": id, "text": text, "from": "owner" if from_owner else "main"}, timeout=60)
    return f"sent to thread {id}" + (" (queued: its lead is mid-turn)" if r.get("queued") else "")


@mcp.tool()
def thread_update(id: str, status: str = "", note: str = "", title: str = "", repo: str = "", branch: str = "",
                  plan: str = "", team: str = "") -> str:
    """(A thread's lead) Keep your thread's record, which the owner sees in the panel (on the thread's card) and Main
    sees in every turn. Execution state (steps, evidence, results, questions) lives in the feature's PLAN.md, in the
    folder the [Feature record] block of your instructions names; this only moves what is already there.
    status: planning | working | needs_you (you are blocked on the owner: note says exactly what you need; they get
    a ping) | done (refused until every PLAN.md step is verified: ticked with evidence and result lines the panel
    checks) | archived. note: where it stands, one line.
    plan: box states only, as a markdown checklist of steps PLAN.md already has, word for word: "- [x] done",
    "- [~] doing now", "- [ ] to do". The panel moves those boxes in PLAN.md and keeps everything else; a line that
    matches no step is refused (add or reword steps by editing PLAN.md). With no PLAN.md yet, the checklist becomes it.
    team: bind your team to a zellij session by its exact live name (the feature's session when it differs from
    yours, e.g. a feature the owner started on the Terminals page); one thread per session, never a guess.
    Only the fields you pass change. A refusal comes back as an error saying why: fix that, don't retry blindly."""
    body = {"id": id}
    for k, v in (("status", status), ("note", note), ("title", title), ("repo", repo), ("branch", branch), ("plan", plan)):
        if v.strip():
            body[k] = v.strip()
    if team.strip():  # one request: the panel binds, then applies plan= to that team's record, or changes nothing
        body["team"] = team.strip()
    t = panel("POST", "/agent/thread/update", body)["thread"]
    if team.strip() and t.get("team") != team.strip():  # a panel without team binding ignores the field
        raise PanelError(f"the panel didn't bind {team.strip()!r}: thread {id}'s team is still {t.get('team')!r}")
    return fmt_threads([t], True)


# ---- a feature's record (featdoc.go in the control panel): one writer for everyone, each write against the
# revision it read ----

def fmt_feature(d):
    """A feature record as text: folder, revisions, conflicts, and every milestone step with its verified state."""
    out = [f"record {d.get('dir', '')}" + (f" · thread {d['thread']}" if d.get("thread") else ""),
           "revs " + " ".join(f"{k}={v}" for k, v in sorted((d.get("revs") or {}).items()))]
    out += [f"CONFLICT {c}" for c in d.get("conflicts") or []]
    for ms in d.get("milestones") or []:
        out.append(f"## {ms['title']}")
        for s in ms.get("steps") or []:
            line = f"- [{s.get('state')}] {s.get('text')}" + (f" @{s['owner']}" if s.get("owner") else "")
            for k in ("label", "why", "blocked", "ask", "attempt", "runAs"):
                if s.get(k):
                    line += f"\n    {k}: {s[k]}"
            out.append(line)
    return "\n".join(out)


@mcp.tool()
def feature_record(session: str, doc: str = "") -> str:
    """(A thread's lead) The feature record of a zellij session (exact name): its folder, each document's revision, and
    every step as the panel verified it (state, label: claimed | check ran (receipt) | owner verified, why, holds,
    questions, and for an owner-approved check its attempt and the exact runAs command). doc: plan | tests | log |
    feature to get that document's whole text too, with the revision to write it back against."""
    r = panel("GET", "/agent/feature" + q(session=session))
    d = r["dossier"]
    out = fmt_feature(d)
    tk = r.get("tickets") if "tickets" in r else d.get("tickets")
    if tk:
        out += "\nOPEN OWNER TICKETS (theirs to answer on the page; you can't close them):\n" + "\n".join(
            f"  {t.get('id')} {t.get('kind')} step {t.get('step') or '-'}: {t.get('title') or t.get('reason') or ''}" for t in tk)
    if doc:
        out += f"\n\n--- {doc} (rev {(d.get('revs') or {}).get(doc, '')}) ---\n" + (d.get(doc) or "")
    return out


@mcp.tool()
def feature_doc_write(session: str, doc: str, text: str, rev: str) -> str:
    """(A thread's lead) Replace the record's PLAN.md (doc="plan") or TESTS.md (doc="tests") with text, if it is still
    at rev (from feature_record). A stale rev is refused and nothing changes: read it again, redo your change, write
    it with the new rev. FEATURE.md is the owner's and LOG.md only grows (feature_log). Approval, verification,
    answers and releases are the owner's, on the feature page; nothing here grants them."""
    r = panel("POST", "/agent/feature/doc", {"session": session, "doc": doc, "text": text, "rev": rev})
    return f"written; {doc} is now at rev {r['rev']}"


def _logged(r, ok="logged"):
    """The log route's answer as said: the line's event id, or the panel's warning that the line is in LOG.md but its
    event wasn't saved (the event log is full awaiting review): never a plain "logged" over a missing event."""
    if (r or {}).get("warning"):
        return f"WARNING: {r['warning']} (the line is in LOG.md; it has no event, so it can't be seen or reviewed on the page)"
    return f"{ok} ({r['id']})" if (r or {}).get("id") else ok


@mcp.tool()
def feature_log(session: str, line: str) -> str:
    """(A thread's lead) Append one dated line to the record's LOG.md (the panel dates it; nothing is rewritten)."""
    return _logged(panel("POST", "/agent/feature/log", {"session": session, "line": line}))


# ---- the manager's typed surface: thin wrappers over the SAME record routes (no other storage) ----
# thread= goes to the panel too, which refuses a mismatch with the current owner (409 {error, thread}) and writes
# nothing: a mistake guard (the right feature, a managed one), not authority: one agent bearer, manager = worker. Approve/verify/answer/release and FEATURE.md stay the owner's (page routes).

def _dossier(session):
    """The feature record (the panel's "dossier") of a zellij session."""
    return panel("GET", "/agent/feature" + q(session=session))["dossier"]


def _managed(session, thread):
    """The record, if thread is the one managing session; else an error saying why (never a write)."""
    d = _dossier(session)
    if not d.get("thread"):
        raise PanelError(f"{session} has no managing thread: a standalone terminal stays unmanaged unless the owner asks "
                         "(then bind it: thread_update(id=…, team=…))")
    if d["thread"] != thread:
        raise PanelError(f"{session} is managed by thread {d['thread']!r}, not {thread!r}")
    return d


def _steps(d):
    """Every step of every milestone of a record, in order."""
    return [s for ms in d.get("milestones") or [] for s in ms.get("steps") or []]


@mcp.tool()
def get_feature_plan(session: str) -> str:
    """(A feature's manager) The feature's PLAN.md: every step as the panel verified it (state, owner, label, why,
    holds, questions, attempt and the exact runAs of an owner-approved check), the text, and the rev to write against."""
    d = _dossier(session)
    return fmt_feature(d) + f"\n\n--- plan (rev {(d.get('revs') or {}).get('plan', '')}) ---\n" + (d.get("plan") or "(no PLAN.md yet)")


@mcp.tool()
def create_feature_plan(session: str, thread: str, text: str) -> str:
    """(A feature's manager) Write the first PLAN.md of the feature your thread manages: milestones as "## …", steps as
    "- [ ] … @Tab" with sub-lines (check:, cwd:, kind:, due:, ask:). Refused when a PLAN.md exists (update_feature_plan)
    or when it would drop steps your thread already published. Checks and kinds you write are proposals: the owner
    approves them on the feature page."""
    d = _managed(session, thread)
    if (d.get("plan") or "").strip():
        raise PanelError(f"{session} has a PLAN.md already (rev {d['revs']['plan']}): use update_feature_plan")
    lost = [s["text"] for s in _steps(d) if s.get("text") and s["text"] not in text]  # the thread's published plan
    if lost:
        raise PanelError("this plan drops steps your thread published: " + "; ".join(lost))
    r = panel("POST", "/agent/feature/doc", {"session": session, "doc": "plan", "text": text, "rev": d["revs"]["plan"], "thread": thread})
    return f"PLAN.md written; rev {r['rev']}"


@mcp.tool()
def update_feature_plan(session: str, thread: str, text: str, rev: str) -> str:
    """(A feature's manager) Replace the PLAN.md of the feature your thread manages with text, if it's still at rev
    (get_feature_plan). Keep re-planning here as you learn: reword, add, split, reassign (@Tab), reorder. A stale rev is
    refused and nothing changes: read it again and redo your change. A ticked box is only a claim; a changed check,
    cwd or owner voids its approval. Requirements (FEATURE.md) aren't here: propose_requirement_change."""
    d = _managed(session, thread)
    if not (d.get("plan") or "").strip():
        raise PanelError(f"{session} has no PLAN.md yet: use create_feature_plan")
    r = panel("POST", "/agent/feature/doc", {"session": session, "doc": "plan", "text": text, "rev": rev, "thread": thread})
    return f"PLAN.md written; rev {r['rev']}"


@mcp.tool()
def get_feature_requirements(session: str) -> str:
    """The feature's requirements: the owner's approved FEATURE.md (desired outcomes), its rev and the outcomes still
    open; and the DRAFT (FEATURE.draft.md) with its rev when one exists. The draft binds nothing until the owner
    promotes it on the feature page."""
    d = _dossier(session)
    revs = d.get("revs") or {}
    text = d.get("feature") or ""
    open_ = [ln.strip()[6:] for ln in text.splitlines() if ln.strip().startswith("- [ ] ")]
    out = (f"APPROVED FEATURE.md (rev {revs.get('feature', '')}; the owner's)\n" + (text or "(none yet)")
           + "\n\nopen outcomes: " + ("; ".join(open_) or "none"))
    draft = d.get("draft") or ""
    return out + (f"\n\nDRAFT FEATURE.draft.md (rev {revs.get('draft', '')}; not approved, binds nothing)\n{draft}" if draft.strip()
                  else "\n\nno draft")


def _draft_write(session, thread, text, rev):
    """Write FEATURE.draft.md against rev (the draft's revision read), with the thread guard."""
    r = panel("POST", "/agent/feature/doc", {"session": session, "doc": "draft", "text": text, "rev": rev, "thread": thread})
    return f"DRAFT written (rev {r['rev']}); the approved FEATURE.md is unchanged until the owner promotes the draft"


@mcp.tool()
def create_draft_requirements(session: str, thread: str, text: str) -> str:
    """(A feature's manager) Start a DRAFT of the feature's requirements (FEATURE.draft.md): the desired outcomes as
    "- [ ] …" lines, what's in and out of scope, why. It's a proposal: the approved FEATURE.md, its outcomes and the
    owner's acceptance don't change, and nothing is planned against it until the owner promotes it on the feature page.
    Refused when a draft exists (update_draft_requirements)."""
    d = _managed(session, thread)
    if (d.get("draft") or "").strip():
        raise PanelError(f"{session} has a draft already (rev {(d.get('revs') or {}).get('draft', '')}): use update_draft_requirements")
    return _draft_write(session, thread, text, (d.get("revs") or {}).get("draft", ""))


@mcp.tool()
def update_draft_requirements(session: str, thread: str, text: str, rev: str) -> str:
    """(A feature's manager) Replace the DRAFT requirements with text, if the draft is still at rev
    (get_feature_requirements). Still only a draft: the owner promotes or discards it on the feature page."""
    _managed(session, thread)
    return _draft_write(session, thread, text, rev)


@mcp.tool()
def propose_requirement_change(session: str, thread: str, change: str, why: str) -> str:
    """(A feature's manager) Propose a change to the owner's requirements (FEATURE.md): it goes to LOG.md for the owner
    to accept on the feature page. Nothing changes until they do; don't plan against it before."""
    _managed(session, thread)
    return _logged(panel("POST", "/agent/feature/log", {"session": session, "line": f"REQUIREMENT PROPOSAL ({thread}): {change} · why: {why}", "thread": thread}),
                   "proposed in LOG.md; FEATURE.md is unchanged until the owner accepts it")


PROGRESS = ("decision", "action", "evidence", "blocker", "research", "progress")


def _field(x, sep="|"):
    """One LOG field: one line, and none of the grammar's delimiters, so text can't forge a segment (" | ", "; ")."""
    x = " ".join(str(x).split()).replace("|", "/")
    return x.replace(";", ",") if sep == ";" else x


LOG_MAX = 2000  # the typed line's limit: a longer line is refused here, never cut
_ESC = {"%": "%25", "|": "%7C", ";": "%3B", ",": "%2C", "=": "%3D", "\n": "%0A", "\t": "%09", "\r": "%0D", " ": "%20"}


def _esc(x):
    """The reversible escaping of "ran" and "next" values, so a command keeps every byte: % | ; , = newline tab CR are
    escaped, and a space too where the log route would collapse it (a run of spaces, or at an edge); a single space
    between words stays literal. One pass, so "%7C" in the text comes back as "%7C". Other whitespace or control
    characters are refused (the route can't keep them). The panel decodes exactly these sequences, in one pass."""
    x = str(x)
    bad = sorted({repr(c) for c in x if c not in _ESC and (c.isspace() or ord(c) < 32 or ord(c) == 127)})
    if bad:
        raise PanelError("a command or next holds characters the log can't keep exactly: " + ", ".join(bad))
    out = []
    for i, c in enumerate(x):
        lone = c == " " and 0 < i < len(x) - 1 and x[i - 1] != " " and x[i + 1] != " "
        out.append(c if lone or c not in _ESC else _ESC[c])
    return "".join(out)


def _unesc(x):
    """The panel's decoding, for the round-trip self-check: one pass over exactly the escaped sequences."""
    back = {v: k for k, v in _ESC.items()}
    return re.sub(r"%(25|7C|3B|2C|3D|0A|09|0D|20)", lambda m: back["%" + m.group(1)], x)


def _ran_item(cmd, result="", ok=None):
    """One "ran:" item, "ok=1|0|?,cmd=<e>,result=<e>": the writer's account of a command, never a receipt."""
    if not str(cmd).strip():
        raise PanelError("each ran item needs its cmd")
    if ok not in (True, False, None):
        raise PanelError("ran ok is true, false or left out (unknown)")
    return f"ok={'?' if ok is None else 1 if ok else 0},cmd={_esc(cmd)},result={_esc(result)}"


def _log_extra(ran=None, next_="", objectives=None, pr=0, proof_step=""):
    """The optional authored segments of a typed LOG line: only what the writer supplied, nothing derived."""
    seg = []
    if ran:
        if not all(isinstance(r, dict) for r in ran):
            raise PanelError('each ran item is {"cmd", "result", "ok"}')
        seg.append("ran: " + "; ".join(_ran_item(r.get("cmd", ""), r.get("result", ""), r.get("ok")) for r in ran))
    if str(next_).strip():
        seg.append("next: " + _esc(next_))
    ids = [str(o).strip() for o in objectives or [] if str(o).strip()]
    for i in ids:
        if not re.fullmatch(r"D\d+", i):
            raise PanelError(f"objective ids are FEATURE.md's D<n>, not {i!r}")
    if ids:
        seg.append("objectives: " + ",".join(ids))
    if pr:
        if not isinstance(pr, int) or isinstance(pr, bool) or pr < 1:
            raise PanelError("pr must be a positive PR number")
        seg.append(f"pr: {pr}")
    if str(proof_step).strip():
        seg.append("proof: " + _field(proof_step))
    return seg


@mcp.tool()
def report_feature_progress(session: str, kind: str, text: str, step: str = "", thread: str = "", rev: str = "",
                            considered: list[str] | None = None, chose: str = "", by: str = "",
                            ran: list[dict] | None = None, next: str = "", objectives: list[str] | None = None,
                            pr: int = 0, proof_step: str = "") -> str:
    """Report to the feature's LOG.md, the public decision/action log the owner reads, as one typed line:
    "KIND: summary | considered: a; b | chose: x | step: <key> | rev: <plan rev> | by: @<tab>".
    kind: decision | action | evidence | blocker | research (typed on the page); progress (a plain note).
    step: the step's key; rev: the PLAN.md revision you're reporting against; by: the tab that did it.
    considered (options weighed) and chose (which, and why) are the public summary you choose to write, never a
    transcript of private reasoning; leave them out rather than invent them. "|" and ";" in your text are replaced, so
    text can't fake a field. A log line verifies nothing: proof goes on the step (add_evidence), and only a receipt or
    the owner verifies. Something only the owner can decide: ask_owner. thread: your thread (the panel refuses a
    feature it doesn't manage).
    Optional, only what you actually have (each omitted when empty; nothing is filled in for you):
    ran: commands you ran, [{"cmd", "result", "ok": true|false}] (ok left out when you don't know), written exactly
    (escaped, never altered): your account, never a receipt, so it verifies nothing; next: the next step you will take; objectives: the FEATURE.md ids it serves
    (["D1"]); pr: the PR number; proof_step: the step key whose proof this concerns. The panel records which session
    wrote the line; you don't supply it."""
    if kind not in PROGRESS:
        raise PanelError("kind must be one of " + ", ".join(PROGRESS))
    extra = _log_extra(ran, next, objectives, pr, proof_step)  # refused before anything is written
    seg = [f"{kind.upper()}: {_field(text)}"]
    if considered:
        seg.append("considered: " + "; ".join(_field(c, ";") for c in considered if str(c).strip()))
    for k, v in (("chose", chose), ("step", step), ("rev", rev)):
        if str(v).strip():
            seg.append(f"{k}: {_field(v)}")
    if by.strip():
        seg.append("by: @" + _field(by).lstrip("@"))
    line = " | ".join(seg + extra)
    if len(line) > LOG_MAX:
        raise PanelError(f"the line is {len(line)} characters, over {LOG_MAX}: say less, or split it (nothing was written)")
    body = {"session": session, "line": line}
    if thread:
        body["thread"] = thread
    return _logged(panel("POST", "/agent/feature/log", body))


_STEP = re.compile(r"^(\s*)- \[(.)\] (.*)$")


def _step_text(line, owner=""):
    """A plan line's step text without its owner tag: the panel's owner can be several words ("@Hermes integration")."""
    line = line.strip()
    if owner and line.endswith(" @" + owner):
        return line[: -len(owner) - 2].strip()
    return re.sub(r"\s+@\S+$", "", line).strip()


def _edit_step(session, thread, step, edit):
    """Read the record, find step (by key) in PLAN.md's text, let edit(lines, at, indent) change it, and write it back
    against the revision read, with the thread guard. A change in between is refused (409); nothing is written."""
    d = _managed(session, thread)
    target = next(((ms.get("title", ""), s) for ms in d.get("milestones") or [] for s in ms.get("steps") or [] if s.get("key") == step), None)
    if target is None:
        raise PanelError(f"no step with key {step!r} in {session}'s plan (get_feature_plan shows the keys)")
    title, st = target
    lines, ms, at = (d.get("plan") or "").split("\n"), "", -1
    for n, ln in enumerate(lines):
        if ln.strip().startswith("## "):
            ms = ln.strip()[3:].strip()
            continue
        m = _STEP.match(ln)
        if m and ms == title and _step_text(m.group(3), st.get("owner", "")) == st.get("text"):
            at = n
            break
    if at < 0:
        raise PanelError(f"couldn't find step {step!r} ({st.get('text')!r}) in PLAN.md's text; read it again")
    edit(lines, at, _STEP.match(lines[at]).group(1) + "  ")
    r = panel("POST", "/agent/feature/doc", {"session": session, "doc": "plan", "text": "\n".join(lines), "rev": d["revs"]["plan"], "thread": thread})
    return r.get("rev", "")


def _sub_end(lines, at):
    """The index just after the indented "- key: value" sub-lines that follow the step at lines[at]."""
    end = at + 1
    while end < len(lines) and re.match(r"^\s+- [a-z-]+:", lines[end]):
        end += 1
    return end


@mcp.tool()
def ask_owner(session: str, thread: str, step: str, question: str, options: str = "", why: str = "",
              whole_feature_blocked: bool = False) -> str:
    """(A feature's manager) Ask the owner something only they can decide, tied to one step (its key from
    get_feature_plan): the step becomes "[?]" with "- ask: <question>" in PLAN.md (written against the revision just
    read; a change in between is refused, nothing written), and it becomes an owner ticket on their feature page: only
    that step waits; the rest of the plan keeps moving (and keeps being woken when idle). Their answer arrives on that
    step only ("answer:"); it authorizes that answer, nothing else. options: the choices, "a | b | c". why: one line on
    what depends on it. whole_feature_blocked: only when NOTHING else can proceed, also set your thread to needs_you."""
    one = lambda x: " ".join(x.split())  # noqa: E731
    ask = one(question) + (f" (options: {one(options)})" if options.strip() else "") + (f" · why: {one(why)}" if why.strip() else "")

    def edit(lines, at, ind):
        m = _STEP.match(lines[at])
        lines[at] = f"{m.group(1)}- [?] {m.group(3)}"
        sub = [k for k in range(at + 1, _sub_end(lines, at)) if lines[k].strip().startswith("- ask:")]
        if sub:
            lines[sub[0]] = f"{ind}- ask: {ask}"
        else:
            lines.insert(at + 1, f"{ind}- ask: {ask}")
    rev = _edit_step(session, thread, step, edit)
    if not whole_feature_blocked:
        return f"asked on step {step} (plan rev {rev}); only that step waits for the owner"
    t = panel("POST", "/agent/thread/update", {"id": thread, "status": "needs_you", "note": ask[:300]})["thread"]
    return f"asked on step {step} (plan rev {rev}); thread {t.get('id')} is {t.get('status')}: nothing else can proceed"


EVIDENCE = ("run", "file", "image", "doc", "db", "link", "ui", "log")


@mcp.tool()
def add_evidence(session: str, thread: str, step: str, kind: str, ref: str, covers: str = "", result: str = "") -> str:
    """(A feature's manager) Put proof under a step (its key) in PLAN.md: "- evidence: <kind> <ref>" and
    "- result: <what it shows>"; covers adds the ids to the step's "- covers:" line (what it serves). kind: run (ref = the attempt of an owner-approved check you ran as runAs) | file |
    image | doc | db | link | ui | log (ref = a path in the record or a worktree of this feature; db and link facts
    count only from a receipt). covers: the requirement ids (D1, D2) it shows met. The panel checks provenance
    mechanically (file there, sealed hash, made by a tool call in this feature after the step's criterion); that
    shows where it came from, not that it's true. It verifies nothing by itself: a receipt or the owner verifies.
    An owner "- proof:" requirement on the step is binding: give that kind. Written against the rev read."""
    if kind not in EVIDENCE:
        raise PanelError("kind must be one of " + ", ".join(EVIDENCE))
    if not ref.strip() or "\n" in ref:
        raise PanelError("ref: one path, attempt or link")
    one = lambda x: " ".join(x.split())  # noqa: E731
    ids = [x.strip() for x in covers.split(",") if x.strip()]

    def edit(lines, at, ind):
        end = _sub_end(lines, at)
        # "log" is a file on the panel (its evidence kinds are run|file|image|doc|db|link|ui): written as file, the
        # purpose kept in the result line, so the panel seals and traces it instead of showing an unparsed line
        k, res = ("file", "log" + (f": {one(result)}" if result.strip() else "")) if kind == "log" else (kind, one(result))
        new = [f"{ind}- evidence: {k} {ref.strip()}"] + ([f"{ind}- result: {res}"] if res else [])
        lines[end:end] = new
        if ids:  # coverage is the step's canonical "- covers: D1, D2" line (merged), never words on the evidence line
            have = next((k for k in range(at + 1, end) if lines[k].strip().startswith("- covers:")), None)
            old = [x.strip() for x in lines[have].split(":", 1)[1].split(",") if x.strip()] if have is not None else []
            merged = ind + "- covers: " + ", ".join(old + [x for x in ids if x not in old])
            if have is None:
                lines.insert(at + 1, merged)
            else:
                lines[have] = merged
    rev = _edit_step(session, thread, step, edit)
    return f"evidence added to step {step} (plan rev {rev}); it shows as claimed until a receipt or the owner verifies it"


ART_PURPOSE = ("ui", "issue")
ART_TITLE_MAX = 200


def _artifact_line(path, purpose, title):
    """The canonical step sub-line "- artifact: <path> | ui|issue | <title>", or PanelError: nothing in a field can
    forge another field or line, and nothing is cut short."""
    p = str(path)
    if not p.strip() or p != p.strip() or "|" in p or any(ord(c) < 32 or ord(c) == 127 for c in p):
        raise PanelError("path: one path, no '|', no newline or control character, no edge spaces")
    if purpose not in ART_PURPOSE:
        raise PanelError("purpose must be one of " + ", ".join(ART_PURPOSE))
    t = " ".join(str(title).split())
    if not t or "|" in t:
        raise PanelError("title: one line of text, no '|'")
    if len(t) > ART_TITLE_MAX:
        raise PanelError(f"title is {len(t)} characters, over {ART_TITLE_MAX}: shorten it (nothing was written)")
    return f"- artifact: {p} | {purpose} | {t}"


@mcp.tool()
def link_artifact(session: str, thread: str, step: str, path: str, purpose: str, title: str) -> str:
    """(A feature's manager) Link a visual artifact (a screenshot, a mockup, an issue capture) to a step (its key) in
    PLAN.md: "- artifact: <path> | ui|issue | <title>", written against the rev read, with the thread guard. The page
    lists it under the step's milestone through the panel's guarded file rules (no secrets, nothing outside the
    feature's folders); nothing is fetched. An artifact is shown, never evidence: it verifies and meets nothing (proof
    goes in add_evidence). The same line already there is not added twice."""
    new = _artifact_line(path, purpose, title)

    def edit(lines, at, ind):
        end = _sub_end(lines, at)
        if not any(lines[k].strip() == new for k in range(at + 1, end)):
            lines[end:end] = [ind + new]
    rev = _edit_step(session, thread, step, edit)
    return f"artifact linked to step {step} (plan rev {rev}); shown on the page, never evidence"


@mcp.tool()
def add_assessment(session: str, thread: str, step: str, verdict: str, reasons: str, cites: str) -> str:
    """(A feature's manager) Record a model assessment of a step's evidence: "- assessment: pass|fail|insufficient —
    <reasons> — cites <evidence refs>". It's a judgment, not proof: it never verifies the step and never accepts
    outcomes; the page shows it as "assessed", labelled by-the-author when whoever wrote it also made the evidence
    (unknown author: independence is never claimed). Any change to the cited evidence or the plan voids it. Prefer an
    agent that didn't make the evidence to assess it."""
    if verdict not in ("pass", "fail", "insufficient"):
        raise PanelError("verdict must be pass, fail or insufficient")
    if not reasons.strip() or not cites.strip():
        raise PanelError("an assessment needs its reasons and the evidence it cites")
    one = lambda x: " ".join(x.split())  # noqa: E731
    line = f"- assessment: {verdict} — {one(reasons)} — cites {one(cites)}"
    rev = _edit_step(session, thread, step, lambda lines, at, ind: lines.insert(_sub_end(lines, at), ind + line))
    return f"assessment recorded on step {step} (plan rev {rev}); shown as assessed, it verifies and accepts nothing"


RELATIONS = ("handoff", "blocks", "pair", "resume")


@mcp.tool()
def assign_feature_job(session: str, thread: str, step: str, tab: str, pane: int = 0, sid: str = "",
                       relation: str = "", relation_with: str = "", why: str = "") -> str:
    """(A feature's manager) Record who works a step (its key; "" for the whole feature): a dispatch event
    (POST /agent/feature/assign) the page's session timeline is built from. tab: the agent's tab (pane and sid when you
    know them). relation: handoff | blocks | pair | resume, with relation_with (the OTHER step's key) and why: the only
    way, with PLAN.md "- after:", that the page shows two jobs related; nothing is inferred from timing. Recording it
    starts nothing (tell the agent with pane_send), and the step's @Tab owner is still PLAN.md's (update_feature_plan)."""
    if not tab.strip():
        raise PanelError("tab: the agent's tab is required")
    body = {"session": session, "step": step, "tab": tab.strip(), "thread": thread}
    if pane:
        body["pane"] = pane
    if sid.strip():
        body["sid"] = sid.strip()
    if relation:
        if relation not in RELATIONS or not relation_with.strip() or relation_with.strip() == step:
            raise PanelError("relation: one of " + ", ".join(RELATIONS) + ", with relation_with = another step's key")
        body["relation"] = {"kind": relation, "with": relation_with.strip(), "why": " ".join(why.split())}
    r = panel("POST", "/agent/feature/assign", body)
    return f"assignment of step {step or '(whole feature)'} recorded ({r.get('id', '')})" + (f", {relation} with {relation_with}" if relation else "")


@mcp.tool()
def get_feature_events(session: str, after: str = "", limit: int = 50) -> str:
    """(A feature's manager) The feature's event log, oldest first after the cursor: step transitions, assignments and
    relations, holds, answers, approvals, reopens, wakes, deliveries, messages. after: the last event id you saw
    ("ev-<n>"); limit up to 200. Reading marks nothing seen. The next cursor is the last line."""
    r = panel("GET", "/agent/feature/events" + q(session=session, after=after or "ev-0", limit=min(max(limit, 1), 200)))
    evs = r.get("events") or []

    def when(e):  # "at" is an RFC 3339 timestamp: show month-day and hour:minute
        return (e.get("at") or "")[5:16].replace("T", " ")

    def line(e):
        rel = e.get("relation") or {}
        who = " ".join(x for x in (f"tab {e['tab']}" if e.get("tab") else "", f"pane {e['pane']}" if e.get("pane") else "",
                                   f"sid {e['sid']} (claimed)" if e.get("sid") else "") if x)
        return (f"{e.get('id')} {when(e)} {e.get('kind')}" + (f" step {e['step']}" if e.get("step") else "") + (f" {who}" if who else "")
                + (f" · {rel.get('kind')} with {rel.get('with')}" + (f" ({rel['why']})" if rel.get("why") else "") if rel else "")
                + (f" @{e['rev']}" if e.get("rev") else "") + (f": {e['text']}" if e.get("text") else ""))
    out = [line(e) for e in evs] or ["no events"]
    if evs:
        out.append(f"next: after={evs[-1].get('id')}" + (" (more)" if r.get("more") else ""))
    return "\n".join(out)


def _team(session):
    """GET /agent/feature/team: the team as of the panel's last completed sample (it doesn't sample for this) and a
    header saying how old that is. lastAt is each agent's last activity, not when it was observed."""
    r = panel("GET", "/agent/feature/team" + q(session=session))
    if not r.get("observedAt"):
        head = "never sampled: the panel has no listing yet (STALE)"
    else:
        head = (f"observed {time.strftime('%Y-%m-%d %H:%M:%S UTC', time.gmtime(r['observedAt']))} ({r.get('ageSec')} s ago"
                + (", STALE: older than the panel's 90 s" if r.get("stale") else "") + ")")
    if not r.get("present"):
        head += f" · {session} is not in that sample: no team visible"
    if r.get("thread"):
        head += f" · thread {r['thread']}"
    return r, head


def _member(r, a, text, dup):
    """One team member as text: identity, activity, git and cost facts, what it is doing and which steps it owns."""
    idle = f"last active {int((time.time() - a['lastAt']) // 60)} min ago" if a.get("lastAt") else "no activity seen"
    bits = [a.get("key", ""), f"tab \"{a.get('tab', '')}\"", a.get("kind", ""), a.get("state", ""), idle,
            f"ctx {a['ctx']}%" if a.get("ctx") else "", f"branch {a['branch']}" if a.get("branch") else "",
            tilde(a.get("root") or ""), f"PRs {a['prs']}" if a.get("prs") else "",
            f"model {a['model']}" if a.get("model") else "", f"on step {a['step']}" if a.get("step") else "",
            f"sid {a['sid']}" if a.get("sid") else ""]
    out = " · ".join(b for b in bits if b)
    if a.get("title"):
        out += f"\n    doing: {a['title']}"
    if a.get("last"):
        out += f"\n    last said: {a['last'][:200]}"
    if a.get("owns"):
        out += "\n    owns: " + "; ".join(f"{k} {text.get(k, '(not in the plan you read)')}" for k in a["owns"])
    if dup or a.get("ambiguousTab"):  # several panes share the tab name: @Tab can't say which of them a step is for
        out += f"\n    AMBIGUOUS: tab name shared with {', '.join(dup) or 'another pane'}; @{a.get('tab')} doesn't pick one of them"
    return out


def _team_lines(session, only=""):
    """The team of a feature's session as text (or just the member whose key is `only`), under a freshness header."""
    r, head = _team(session)
    text = {s.get("key"): f"[{s.get('state')}] {s.get('text')}" for s in _steps(_dossier(session))}
    tabs = {}
    for a in r.get("team") or []:
        tabs.setdefault((a.get("tab") or "").lower(), []).append(a.get("key", ""))
    team = [a for a in r.get("team") or [] if not only or a.get("key") == only]
    if only and not team:
        raise PanelError(f"no agent {only} in {session}'s team ({head})")
    out = [f"{head} · {len(r.get('team') or [])} agents"]
    for a in team:
        dup = [k for k in tabs[(a.get("tab") or "").lower()] if k != a.get("key")]
        out.append(_member(r, a, text, dup))
    return "\n".join(out)


@mcp.tool()
def get_feature_team(session: str) -> str:
    """(A feature's manager) Who is on the feature's team, as of the panel's last sample: line 1 says when that was, how
    old, and STALE when over 90 s or never. Per agent: key (session:pane), tab, kind, state, last activity, context
    use, branch, folder, PRs, what it's doing and the PLAN.md steps it owns (@Tab, exact tab name). "working" with old
    activity is a long tool call, not idleness; check get_team_member before nudging. Shared tab names are flagged."""
    return _team_lines(session)


@mcp.tool()
def get_team_member(session: str, pane: int, last: int = 10) -> str:
    """(A feature's manager) One team member as get_feature_team shows it, plus its last few transcript items. Talk to
    it with pane_send; a question on its screen: pane_screen."""
    out = _team_lines(session, f"{session}:{pane}")
    try:
        items = panel("GET", "/agent/pane/chat" + q(session=session, pane=pane, n=last)).get("items") or []
    except PanelError as e:  # e.g. no transcript (a Codex pane): the state above still stands
        return out + f"\n\n(no transcript: {e})"
    return out + "\n\n" + fmt_chat(items)


# ---- visual cards: message-ui (React → PNG, iMessage-style) in this repo's attachments/ folder ----

ATTACH = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "attachments")


def _cards(*args, timeout=120):
    """Run attachments/render.tsx with node (through tsx) and the given arguments; returns the CompletedProcess."""
    node = shutil.which("node") or "/usr/local/bin/node"
    return subprocess.run([node, os.path.join(ATTACH, "node_modules/tsx/dist/cli.mjs"), "render.tsx", *args],
                          cwd=ATTACH, capture_output=True, text=True, timeout=timeout)


@mcp.tool()
def card_templates() -> str:
    """The visual cards you can render for the owner (message-ui: dark, Inter, iMessage-style images that look the
    same in the panel and on Telegram): each card's name, what it is for, its size and sample props (JSON).
    Render one with render_card."""
    r = _cards("--list")
    return r.stdout if r.returncode == 0 else "couldn't list cards: " + r.stderr[-1500:]


@mcp.tool()
def render_card(card: str, props: dict, width: int = 0, height: int = 0) -> str:
    """Render a visual card to a PNG with your data, and get the line that shows it.
    card: a name from card_templates (metric-card: a number with its trend line; breakdown-card: a donut and its
    legend; rings-card: three goals as activity rings; list-card: a status list), or the path of your own .tsx
    template (a default-export React component built from @message-ui/components, inline styles, with static
    PreviewProps and PreviewSize; see attachments/cards in the vm-agent repo for examples).
    props: the card's data, merged over its sample props. width/height: override the canvas (logical px).
    In the panel put the returned ![…](path) line in your answer; on Telegram put the MEDIA: line on its own line."""
    args = [card, "--props", json.dumps(props)]
    if width:
        args += ["--width", str(width)]
    if height:
        args += ["--height", str(height)]
    r = _cards(*args)
    if r.returncode != 0:
        return "render failed: " + (r.stderr or r.stdout)[-1500:]
    path = r.stdout.strip().splitlines()[-1]
    return f"Rendered {path}\nPanel: ![{props.get('title', card)}]({path})\nTelegram: MEDIA:{path}"


if __name__ == "__main__":
    mcp.run()
