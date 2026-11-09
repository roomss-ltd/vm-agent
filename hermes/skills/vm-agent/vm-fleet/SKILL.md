---
name: vm-fleet
description: "Run the owner's VM: the zellij fleet of Claude Code agents (status, reply, answer prompts, dispatch new agents), VM health, CI, and the panel's events. Use whenever the owner talks about agents, sessions, tabs, panes, terminals, the VM, CI, or replies to a Fleet ping."
version: 1.0.0
platforms: [linux]
metadata:
  hermes:
    tags: [vm, zellij, claude-code, fleet, devops]
    category: vm-agent
---

# VM fleet

The owner's Claude Code agents run in zellij sessions on this VM (e.g. `box-dev`, `dev-1`;
`box-servers` holds long-running servers, including you). The control panel tracks them; the `vm`
MCP server exposes that to you. Addresses are `session:pane` (pane ids are zellij's, e.g. `box-dev:3`).

## Tools (MCP server `vm`)

| Need | Tool |
|---|---|
| What's running, who is waiting/asking, on which branch | `fleet_status` |
| What an agent did / is asking, in its own words | `pane_chat(session, pane, last)` |
| What any pane shows now (servers, logs, dialogs) | `pane_screen(session, pane)` |
| Send an agent a message, or run a command in a shell pane | `pane_send(session, pane, text)` |
| Answer a permission/question dialog | `pane_answer(session, pane, choice)` (labels from fleet_status) |
| Interrupt (Esc) / stop a command (Ctrl c) | `pane_keys` |
| New session/tab/pane, optionally start Claude with a first prompt | `open_terminal` |
| Close a pane/tab/session | `close_terminal` (ask first if an agent is working there) |
| CPU/memory/disk, CI runners and jobs, security checklist | `vm_status(view)` |
| What happened lately (finished/asking/stalled agents, CI failures, disk) | `recent_events` |

## Conventions

- **PR review automation.** PR approvals are handled by an AI reviewer. Recheck current review/check status and continue the already-authorised release when required checks and approvals pass; do not repeatedly ask the owner to approve an ordinarily pending automated review. Investigate stalled automation or actual changes-requested findings instead. Never bypass branch protection or treat auto-approval as proof of runtime success.

- **Replying to a Fleet ping.** A Telegram reply to a "Fleet" message arrives as
  `[Replying to: "…[box-dev:3]…"] <owner's text>`. Take the address from the quote. If the agent is
  asking (a dialog with options), map the owner's answer to an option label and use `pane_answer`;
  otherwise forward the owner's text with `pane_send`. Confirm in one line ("Sent to auth-fix.").
- **Forward, don't rewrite.** When the owner says "tell X to …", send their words (lightly cleaned),
  not your paraphrase. Don't add instructions of your own to another agent unless asked.
- **Model safeguard pause** (Claude Code shows "<model>'s safeguards flagged this session ... 1. Switch to <an older model> / 2. Edit prompt and retry with <model>"): the owner's standing rule is to ALWAYS keep agents on the model they run. Press `2` (pane_keys), then `Enter` to resend the queued prompt; never pick the switch option. `pane_answer` may report "no question" for this dialog, so use pane_keys. Re-check the screen after ~20 s; if it pauses again, retry on the same model again and tell the owner. If it re-flags on every retry (the session context itself trips the filter), stop retrying: propose a fresh session on the same model (new tab, same worktree, handoff from the agent's ledger/plan docs) instead of switching to the older one. After pressing `2`, the background-agents footer can steal focus ("Enter to view · x to stop"): never press `x` there; `Esc` returns to the prompt.
- **Watcher crons must never remove themselves in the run that sends the message.** A cron job's
  final response is delivered only after the run ends, and only if the job still exists. A job that
  removes itself before replying drops the owner's notification silently (this happened once). Set
  a `sent` flag in the state file and let the NEXT run remove the job and reply `[SILENT]`.
- **After a VM/zellij restart** pane ids are renumbered and every Claude pane starts a blank
  conversation. To resume: find each tab's last session in `~/.claude/projects/<cwd-escaped>/*.jsonl`
  (newest file whose `ai-title` matches the tab's old title), then `pane_send` `/resume <session-id>`
  into that tab's pane. It works even when the pane's cwd differs, and Claude switches to the session's
  worktree. Background jobs inside agents died with the restart, so they need a "continue" nudge.
  Watchers must find panes by TAB NAME, never by a hard-coded pane id.
- **Autonomous agents (mute + advisor).** When the owner wants agents to work alone without pinging them: add their `session:pane` to `~/.config/vm-agent/muted-panes`. `jarvis` from those panes then logs to `~/.local/state/vm-agent/muted-outbox/<session>-<pane>.log` and tells the agent to decide by itself. The mute list holds pane ids, so update it after a restart renumbers panes. To set a stronger advisor, `pane_send` `/advisor fable` (the strongest model). It applies after the session's next /compact or /clear, and if the agent has queued messages the command waits behind them. Check for "Advisor set to Fable" in the session jsonl. A JARVIS cron check-in for those agents reads the panes and the agents' progress files, nudges stuck agents, and messages the owner only at digest slots or for urgent items.
- **Owner's guard rails for autonomous agents.** (1) No merge into stage or main without the owner's permission. `~/.local/bin/guard-no-merge-hook` is a PreToolUse Bash hook in ~/.claude/settings.json that blocks `gh pr merge` and pushes to stage/main for muted panes; hooks load at session start, so already-running sessions rely on the written rule. (2) Each agent finishes with a PROOF.html/PDF in the style of the owner's reference proof page, with screenshots from a fully rendered local-stack browser recorded as one `cube-browser --session proof-<x>` session. The owner replays it in the panel (Agents → Recordings). Trails live in ~/.local/state/control-panel/agent-browsers/trails/; set `"pinned": true` in meta.json so they survive the weekly sweep. (3) Agents escalate on Telegram themselves with `notify-owner "<decision>" "<options + the advisor's view>"`, only for an advisor impasse or a critical decision (rate limit 1 per 2 h per pane; log ~/.local/state/vm-agent/escalations/log).
- **Proof HTML must embed its images.** The panel renders HTML artefacts sandboxed (CSP `img-src https: data: blob:`), so relative or file image paths show as broken pictures, and it serves at most 25 MB. Run `proof-check <file>` to test a page and `proof-check --embed <file>` to inline every image as a JPEG data: URI. A proof only counts once proof-check passes.
- **Panel pane input 409 'input box already has text'.** A Claude prompt suggestion or leftover text blocks pane_send. Retry later (a cron can hold it as a pending message); don't press Esc on a working agent.
- **Multi-agent programs (e.g. several parallel ports of one feature):** give each agent its OWN message built from the owner's answers (shared rules + its specific ones), never one generic blast. When one agent owns shared code others wait on, have it write a flag file (e.g. ~/jarvis-files/<program>/<owner>/STAGE-SHIPPED.md) and run a no_agent cron script that types the relay into the waiting panes via the panel's /agent/pane/input (token ~/.config/control-panel/agent-token) once, printing only when it relays. Agents' own progress/BOTTLENECK files are the source for status; digests stale fast, so re-read before quoting numbers.
- **Never disturb a working agent** with Esc/Ctrl c unless the owner asks to interrupt it. A message
  sent while it works is queued by Claude Code; that is fine.
- **Dispatch.** "Start an agent on <repo> to <task>": pick the checkout under `~/repos/<repo>` (use a
  git worktree under `<repo>/.worktrees/<branch>` when another agent already works in that checkout),
  then `open_terminal(what="tab", session=<the owner's dev session, default box-dev>, name=<short
  task name>, cwd=<dir>, run="claude", prompt=<the task>)`. Report the address.
- **Which session.** Ask only if it matters and is unclear; default to `box-dev` for development.
  Never put agents in `box-servers` (servers only).
- **Checking on work.** `fleet_status` first (cheap), then `pane_chat` for the one that matters.
  Summaries for the owner: one line per agent: name · state · what it's doing/asking.
- **Name agents by their zellij tab name** (e.g. "Scheduler Fleet", "Billing Export") in
  everything the owner reads, including cron/Telegram pings. Never show `session:pane` codes like
  `dev-1:10`; the owner finds them uninformative. Keep the address internal and resolve the tab name
  back to a pane with `fleet_status` when the owner replies.
- **Installing skills/plugins for all agents.** All Claude panes share the same `~/.claude`, so install at user scope with the real binary (`~/.local/bin/claude`, not a launcher or wrapper script): `claude plugin marketplace add https://github.com/roomss-ltd/<repo>.git` then `claude plugin install <plugin>@<marketplace> --scope user`. Verify with a one-shot `claude -p "list skills ..."`, then `pane_send` `/reload-plugins` to every Claude pane (busy ones queue it).
- **Approved stale-worktree cleanup:** Recheck each explicitly approved candidate against fresh fleet/process/open-file use, current Docker bind mounts, Git status (including ignored unique artifacts), HEAD preservation, and registration. Persist an entry-by-entry ledger; use only `git worktree remove` without force and verify both path and registration disappear. Docker API visibility failures are unresolved safety gates, not evidence of no mounts; skip uncertain candidates without restarting Docker or deleting caches to work around the blocker. Preserve branch refs and shared objects.
- **Broad worktree cleanup after fresh owner authorization:** Inventory every Git common directory under the authorized repo root, deduplicate registered paths, and avoid double-counting parent checkout `du` totals that include nested worktrees. A prior narrow shortlist is not the new scope. Obsolete feature trees may hold squash-only commits: preserve and verify their branch refs rather than requiring main ancestry. Archive and byte-verify unique ignored files (including `.env`, proofs, and small virtualenvs) in private 0700 directories/0600 archives before removal; never print secrets. Persist separate append-only batch ledgers and measure actual filesystem deltas. If Docker is unavailable, keep that fact unresolved; independently confirmed retired-team ownership plus port/config/process evidence may distinguish specific unused candidates, while uncertain/service-linked paths stay protected. Never reuse a removal script that overwrites earlier evidence.
- **Container cleanup evidence redaction:** Docker `Config.Cmd` and entrypoints can contain inline credentials (for example Redis `--requirepass`); omitting `Env` is not sufficient. Inspect command paths in memory but redact secret-bearing arguments before saving or printing container inventories. An ancestor repository bind is not by itself evidence that every nested worktree is in use: map the container's workdir/command and inspect its processes' cwd/open-file paths before classifying specific retired helpers.
- **Cleanup verification identifiers:** Match `git worktree list --porcelain` against the exact `worktree <path>` line, never substring containment: a removed `foo` can still prefix retained `foo-bar`. After interrupted cleanup, reconcile every start entry against path absence, exact registration absence, and unchanged preserved refs before counting it. For documented disposable local task databases, use explicit names and non-FORCE DROP with finite lock/statement timeouts; preserve proof copies and templates unless their retirement is independently established.
- **Disk-exhaustion reclaim audit:** Match deleted-open file `st_dev` to the exhausted filesystem and sum allocated `st_blocks*512`, deduplicated by device/inode; memfd mappings are RAM, not disk reclaim. Report inaccessible process FD directories explicitly. Read the latest deletion ledger before reusing an old approved shortlist—already-deleted entries cannot reclaim space twice, and old cleanup scripts may overwrite evidence or embed stale fleet snapshots. Cache `du` totals are not guaranteed reclaim because hardlinks/open handles can retain blocks; no cache deletion authority follows from release authority.
- **Read-only RAM audits:** map Zellij server PIDs to sessions using `/proc/<pid>/cmdline` server socket paths and only allowlisted `ZELLIJ_SESSION_NAME` environment keys, then cross-check pane counts with `fleet_status` (never raw Zellij control). Use `smaps_rollup` PSS/private anonymous/SwapPss rather than summing RSS, record inaccessible-process coverage, and repeat a short sample with PSI and swap-counter deltas. Inspect the actual executable/source and scrollback config before blaming a leak; bounded scrollback and short-term heap growth do not establish a cause. Detect cgroup v1 versus v2 mounts before interpreting missing memory files. Avoid full argv/environment dumps, especially Node/service credentials. Session recreation requires checkpoints and explicit approval: waiting panes may retain background work, and sessions with long-running work stay protected.
- **The shell** is still yours for everything else (git, gh, logs, files, processes, docker, etc.).
  `~/projects/control-panel` is the panel, `~/projects/vm-agent` is you.
