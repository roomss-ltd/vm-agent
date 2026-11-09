# Mechanisms

Every moving part that keeps JARVIS running or keeps it in check, each with **what** it is, **why** it exists, **where**
it lives, and **how to check** it. Paths are relative to this repo unless they start with `~`. For the big picture see
[ARCHITECTURE.md](ARCHITECTURE.md); for the threads framework see [THREADS.md](THREADS.md).

Contents:
[ensure-agent.sh and run-agent.sh](#ensure-agentsh-and-run-agentsh-staying-alive) ·
[setup.sh](#setupsh-idempotent-configuration) · [fleet pings](#fleet-pings-the-cron-job) ·
[Telegram buttons](#telegram-reply-buttons-the-vm-telegram-plugin) · [voice notes](#voice-notes-speech-to-text) ·
[jarvis CLI](#jarvis-cli-one-conversation-per-pane) · [guard-no-merge-hook](#guard-no-merge-hook) ·
[notify-owner](#notify-owner-escalation-with-a-rate-limit) · [proof-check](#proof-check) ·
[visual cards](#visual-cards-attachments) · [reply blocks](#reply-blocks-tables-buttons-cards-artifacts) ·
[contract tests](#contract-tests) · [secrets](#secrets-handling) · [panel-side mechanisms](#mechanisms-that-live-in-the-panel)

---

## ensure-agent.sh and run-agent.sh: staying alive

**What.** `run-agent.sh` is the assistant in the foreground. It runs `setup.sh`, then two supervision loops:

- the dashboard: `hermes dashboard --host 127.0.0.1 --port 9119 --no-open --skip-build`, restarted 2 s after it exits;
- the gateway: `hermes gateway run --external-supervisor --replace`. Exit code 75 (an in-chat `/restart` or an update)
  relaunches at once; any other exit relaunches after 2 s. Ctrl-C stops both for good (a trap kills the dashboard loop).

Both run with stdin from `/dev/null`, so Hermes never stops to ask a question on a console nobody watches (an
"Install it now? [Y/n]" prompt would hang the turn). `HTTPS_PROXY` and friends are unset: Telegram and the model APIs
go direct.

`ensure-agent.sh` is for your shell profile. It is idempotent: if a process of your user is already running
`gateway run --external-supervisor` (in a tab, or started earlier), it exits silently; otherwise it starts
`run-agent.sh` in the background with `nohup`, logging to `run/agent.log`.

**Why.** Hermes' own service installers need systemd or launchd. A dev VM that has neither (or that you do not want to
give root to) still needs the assistant to come back after a reboot or a crash. This is the same pattern the control
panel uses (`ensure-panel.sh` / `run-panel.sh`).

**Where.** `run-agent.sh`, `ensure-agent.sh`. The usual setup keeps `run-agent.sh` in a tab of a zellij session so its
log scrolls there (a tab in a zellij session reserved for servers; the script comments call it `box-servers`), with `ensure-agent.sh` as the
fallback called from `~/.zshrc` (or `~/.bashrc`).

**Check.**

```sh
pgrep -fa "gateway run --external-supervisor"   # the gateway is up (and which user runs it)
hermes status                                    # "Gateway: running", platforms api_server and Telegram
tail -f ~/projects/vm-agent/run/agent.log        # only when ensure-agent.sh started it in the background
```

To restart after changing prompts, skills, the MCP server or the plugin: use **Restart Gateway** in the dashboard, or
kill the gateway process and let the loop relaunch it, or Ctrl-C the tab and run `./run-agent.sh` again. A change to
`setup.sh` itself needs the last option (or just `./setup.sh`), because only `run-agent.sh` start runs it.

---

## setup.sh: idempotent configuration

**What.** One script that installs Hermes if it is missing (the upstream installer, `--skip-setup --skip-computer-use`)
and makes `~/.hermes` match this repo. You can run it as often as you like; every step first checks whether it is
already done:

| Step | Idempotence |
|---|---|
| `hermes pm install --extra ...` (anthropic, telegram, google, mcp, cron, web, ddgs, doc-extract, vision, pty, stt-whisper) | installs optional Hermes features up front so Hermes never asks on the console |
| `npm ci` in `attachments/` | only if `attachments/node_modules` is missing |
| `~/.config/vm-agent/api-key` | created once (32 random bytes, hex, mode 0600 in a 0700 dir); `API_SERVER_KEY`, `API_SERVER_ENABLED=true`, `API_SERVER_HOST=127.0.0.1`, `API_SERVER_PORT=8642` set only when they differ |
| `TELEGRAM_HOME_CHANNEL` | set to the first id in `TELEGRAM_ALLOWED_USERS`, only if it is unset |
| `approvals.mode off`, `approvals.cron_mode approve`, `approvals.unattended_mode approve`, `terminal.cwd "$HOME"` | `hc` helper sets a key only if its value differs |
| `stt.*` | see [voice notes](#voice-notes-speech-to-text) |
| symlinks | `~/.hermes/SOUL.md`, `~/.hermes/LEAD.md`, `~/.hermes/plugins/vm-telegram`, `~/.local/bin/{jarvis,feature,feature-doc}`, `~/.claude/skills/jarvis`, and `~/.local/bin/uv` (so JARVIS's shell finds it); `ln -sfn` |
| `skills.external_dirs`, `mcp_servers.vm.command/args` | point Hermes at `hermes/skills/` and at `uv run --quiet --script mcp/vm_mcp.py` |
| `hermes plugins enable vm-telegram` | only if not already in `plugins.enabled` |
| fleet pings | `hermes/scripts/*.py` copied to `~/.hermes/scripts/`, `cron.wrap_response false`, `cron.mirror_delivery true`, and the `fleet-pings` cron job created only if `hermes cron list` does not show it |

It **never asks for secrets**. Model keys, the Telegram bot token and the allowed user ids are entered in the Hermes
dashboard or with `hermes config set NAME value` (upper-case names land in `~/.hermes/.env`).

**Why.** The repo is the source of truth for behaviour (`SOUL.md`, `LEAD.md`, skills, tools); `~/.hermes` is where
Hermes reads it. Symlinks mean an edit takes effect on the next gateway restart with no copy step; running setup on every
start (`run-agent.sh`) means a fresh checkout converges to the same machine.

**Also done:** `notify-owner`, `guard-no-merge-hook`, `proof-check` and `fable-advise` are linked into `~/.local/bin`.
**Not done by setup.sh:** registering the guard hook in `~/.claude/settings.json` (by hand, see "Optional extra: the merge
guard" in [the README](../README.md#first-time-configuration)).

**Where.** `setup.sh`. **Check.**

```sh
./setup.sh                                  # ends with "vm-agent: Hermes configured"; a second run changes nothing
hermes config get approvals.mode            # off
hermes config get mcp_servers.vm.command    # the uv path
ls -l ~/.hermes/SOUL.md ~/.hermes/LEAD.md ~/.hermes/plugins/vm-telegram
hermes cron list                            # a job named fleet-pings
```

---

## Fleet pings: the cron job

**What.** A Hermes cron job named `fleet-pings` (`every 1m`, `--no-agent`, `--deliver telegram`,
`--failure-deliver local`) runs `hermes/scripts/fleet_pings.py` once a minute. The script asks the panel for its notes
(`GET /agent/notes`), picks the ones newer than the last it saw, and **prints** them; whatever it prints is delivered to
the Telegram home chat, and printing nothing sends nothing. No model is involved (`--no-agent`).

- Kinds sent: `done, asking, stalled, ci, sec, disk, mem, thread` (override with `VM_AGENT_PING_KINDS`, a comma list).
- At most the last 10 new notes per message; the first run starts from "now" so history is not replayed; if the
  panel's note ids were reset, it starts again from the newest note.
- Each line carries an address the reply can use: `[<session>:<pane>]` for a pane, `[thread:<id>]` for a lead that needs
  the owner. For an `asking` agent the ping includes the options of the dialog on its screen
  (`GET /agent/pane/screen`), so the owner can answer from the phone.
- Panes that belong to a thread's team are skipped: their news goes to their lead (the panel hands it over), not to Telegram.
- State: `~/.local/state/vm-agent/pings.json` (`{"last": <note id>}`).

`cron.mirror_delivery true` copies the delivered text into the Telegram DM's session, so the owner's reply
("Replying to: ...[dev:3]...") reaches Main with the context. Main then maps the answer to a dialog option
(`pane_answer`) or forwards the words (`pane_send`), as the `vm-fleet` skill says. `cron.wrap_response false` removes
Hermes' own wrapper text.

**Why.** The panel already knows when an agent finishes, asks or stalls and when CI, disk or memory go bad; this puts those
on the owner's phone with the means to answer, without a model call per minute.

**Where.** `hermes/scripts/fleet_pings.py` (copied to `~/.hermes/scripts/`, the only place Hermes runs cron scripts from),
created by `setup.sh`. Latency is up to about a minute (the cron tick).

**Check.**

```sh
hermes cron list                                  # fleet-pings, every 1m
cat ~/.local/state/vm-agent/pings.json            # compare "last" with the newest id from the vm tool recent_events
```

Do not run the script by hand to test it: it advances the cursor and prints the message instead of sending it, so the
owner would miss those pings.

---

## Telegram reply buttons: the vm-telegram plugin

**What.** A Hermes plugin (`hermes/plugins/vm-telegram`, enabled by `setup.sh`). When a reply ends in a ` ```buttons `
block (the same JSON the panel reads), the block leaves the text and becomes **inline buttons under that message**:

```json
[{"label": "Yes, build it", "send": "Yes, build cuts 1-4"}, {"label": "Open the PR", "url": "https://github.com/example-org/example-repo/pull/1"}]
```

- `send` buttons answer for the owner: a tap becomes a message from them, quoting the message it was under;
  `url` buttons open the link; the panel-only `open` buttons are dropped on Telegram.
- Rules: at most 8 buttons, labels cut at 60 characters, short labels (18 or fewer) two to a row; a block that does not
  parse as JSON is removed from the text rather than shown raw.
- After a tap the buttons are replaced by a single "check mark + label", and the choice is gone (one answer per message).
  A tap from a user who is not in `TELEGRAM_ALLOWED_USERS` is refused. Taps still work after a restart (button sets are
  kept, newest 300, in `~/.local/state/vm-agent/tg-buttons.json`); an expired one says "That choice has expired: reply in words".

**Why.** On a phone, "yes / no / open it" is a tap, not a typed sentence. Hermes has no hook for outgoing messages, so
the plugin wraps the Telegram adapter's `send()` (final replies, cron deliveries and fleet pings all go through it) and
registers its own callback handler for `vb:` buttons.

**Where.** `hermes/plugins/vm-telegram/{plugin.yaml,__init__.py}`; test `test_vm_telegram.py`.

**Check.**

```sh
hermes config get plugins.enabled                 # contains vm-telegram
ls -l ~/.hermes/plugins/vm-telegram
# the self-test needs the Hermes virtualenv that has python-telegram-bot (the 'telegram' extra); not every
# ~/.hermes/installs/*/environments/* does
for v in ~/.hermes/installs/*/environments/*/venv/bin/python; do
  "$v" -c "import telegram" 2>/dev/null && "$v" hermes/plugins/vm-telegram/test_vm_telegram.py
done
```

---

## Voice notes: speech to text

**What.** Telegram voice notes are transcribed on the VM by Hermes' local speech-to-text, then handled as text. `setup.sh` sets
`stt.enabled true`, `stt.provider local`, `stt.local.model small` (faster-whisper on CPU; the setup comment measures about
5 s for 10 s of speech), `stt.local.unload_after_idle_seconds 1800` (the model is let go after 30 idle minutes), and
`stt.prompt`, a short list of names the model would otherwise mishear. The `stt-whisper` extra is installed by the
`hermes pm install` line. `SOUL.md` tells JARVIS the transcript can mishear names and to check before acting on a
doubtful one.

**Why.** Voice is how people drive a VM from a phone; local keeps audio off third-party APIs and costs nothing per note.

**Where.** `setup.sh` (the `stt.*` block). Edit `stt.prompt` for your own project and tool names.

**Check.** `hermes config get stt.provider` (local), then send a voice note. (The panel has its own voice input for the Assistant
chat, in the control-panel repo.)

---

## jarvis CLI: one conversation per pane

**What.** `bin/jarvis` (linked to `~/.local/bin/jarvis`) is how a terminal, usually a Claude Code agent, talks to JARVIS:

```sh
jarvis "PR #212 is green and ready for review"      # waits up to 90 s for the answer and prints it
jarvis --no-wait "nightly import done: 1,204 rows"  # send and return
some-report | jarvis -                               # message from stdin
jarvis --wait 300 "..."                              # wait longer (seconds)
jarvis --resend <file>                               # send again a report saved as "Not sent"
```

Routing, in order:

1. **A muted pane** (its `session:pane` is a line in `~/.config/vm-agent/muted-panes`): the message is parked in
   `~/.local/state/vm-agent/muted-outbox/<session>-<pane>.log` for JARVIS's next check-in, never posted to the owner, and the
   agent is told to decide by itself.
2. **A thread's team** (the pane's zellij session is a live thread's `team`, looked up in `GET /agent/threads`): the message
   goes to that thread's lead via `POST /agent/thread/send` and the call returns at once; the lead answers in the pane as
   `[From your lead]`. If the panel cannot be asked after three quick tries, nothing is guessed: the message is saved with the
   pane's identity in `~/.local/state/vm-agent/unsent/` and `jarvis --resend <file>` (from that same pane) sends it later.
3. **Everything else:** the pane gets its own conversation with JARVIS, a Hermes session `agent-<session>-<pane>` (a terminal
   outside zellij shares one called `agent-shell`) on the API server (`POST /api/sessions`, then `/api/sessions/<id>/chat`), so JARVIS remembers what that pane told it and the owner
   can read it in the panel. The text JARVIS sees starts `[From the Claude agent in <session>:<pane>, working in <dir>]`.
   A detached child holds the turn so it finishes whatever the caller does; if the answer takes longer than `--wait`, the
   command returns and the answer is typed into the pane later as a message starting `[From JARVIS]`.

JARVIS writes back with the `pane_send` tool (text starting `[From JARVIS]`). The Claude Code skill
`claude/skills/jarvis/SKILL.md` (linked into `~/.claude/skills`) tells agents when to use the command and how to read a
`[From JARVIS]` message.

**Why.** Agents need a way to say "I am blocked on a decision" or "the thing you are waiting for is done" that reaches a person
without every agent holding a Telegram token, and a lead needs to hear from its team without polling.

**Where.** `bin/jarvis`; state under `~/.local/state/vm-agent/` (`agent-replies/`, `unsent/`, `muted-outbox/`); env overrides
`JARVIS_URL` (default `http://127.0.0.1:8642`) and `PANEL_AGENT_URL` (default `http://127.0.0.1:7778`).

**Check.** `python3 bin/test_jarvis.py` (a stand-in Hermes and panel; prints `ok`). Live: `jarvis --no-wait "test"` from a pane, then
look for a conversation named "Claude agent <session>:<pane>" in the panel.

---

## guard-no-merge-hook

**What.** A Claude Code `PreToolUse` hook for the `Bash` tool. For a pane listed in `~/.config/vm-agent/muted-panes` (one
`session:pane` per line and nothing else on the line: the hook matches whole lines), it reads the hook's JSON from stdin and exits **2**
(block, message shown to Claude) if the command looks like `gh pr merge`, a `gh api .../merge` call, a `git push` to
`stage`/`main`/`master`, or a force-push to `stage`/`main`. Any other pane, and any other command, passes with exit 0.

**Why.** Autonomous agents may open and update pull requests but must not land code: merging into `stage`/`main` is the
owner's call.

**Where.** `bin/guard-no-merge-hook`. `setup.sh` links it into `~/.local/bin` but does **not** register it. Register it
in `~/.claude/settings.json` (use an absolute path; Claude Code loads hooks at session start, so already
running sessions need a restart):

```json
{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/home/you/.local/bin/guard-no-merge-hook"}]}]}}
```

Adding a pane to the mute list is a line `session:pane` in `~/.config/vm-agent/muted-panes`; pane ids are zellij's, so
update it after a restart renumbers panes.

**Limits.** It guards Claude Code panes that are on the mute list only. A thread's team is not muted automatically: it is
bound by the written rule in `LEAD.md`. Pattern matching is a guard rail, not a sandbox.

**Check.**

```sh
T=$(mktemp -d); mkdir -p $T/.config/vm-agent; echo "s:1" > $T/.config/vm-agent/muted-panes
echo '{"tool_input":{"command":"gh pr merge 12"}}' | HOME=$T ZELLIJ_SESSION_NAME=s ZELLIJ_PANE_ID=1 bin/guard-no-merge-hook; echo $?   # 2
echo '{"tool_input":{"command":"git status"}}'     | HOME=$T ZELLIJ_SESSION_NAME=s ZELLIJ_PANE_ID=1 bin/guard-no-merge-hook; echo $?   # 0
```

---

## notify-owner: escalation with a rate limit

**What.** `bin/notify-owner "<decision needed>" "<options, the advisor's view, what it blocks>"` sends a Telegram message
(`hermes send -t telegram`) starting "Decision needed" with the pane's folder name. Both arguments are required (exit 2
otherwise). It is **rate-limited to one message per pane per 2 hours**: a second call inside the window prints "Not sent" and
exits 3, telling the agent to decide with its advisor or note the question in `OPEN-QUESTIONS.md`. Every sent message is
appended to `~/.local/state/vm-agent/escalations/log` for JARVIS to read; the last-sent time per pane is a
`<session>-<pane>.last` file next to it.

**Why.** Autonomous agents that cannot ask a lead need an exit for a real impasse, but an agent that pings on every doubt is
worse than none. The limit and the "only for an impasse or a critical decision" wording keep it rare.

**Where.** `bin/notify-owner` (linked into `~/.local/bin` by `setup.sh`).
Needs `hermes` on PATH and a Telegram home channel.

**Check.** `ls ~/.local/state/vm-agent/escalations/` and `tail ~/.local/state/vm-agent/escalations/log`. Calling it sends a real
message; to test only the argument check, run it with no arguments (usage, exit 2).

`bin/fable-advise "<question>"` is the optional companion for the "advisor" half: a read-only, no-tools run of a stronger model
(`teamclaude run -p --tools ""`, 600 s cap via `FABLE_TIMEOUT`, the model named inside the script) that prints a
recommendation. It needs a `teamclaude` launcher on PATH (or edit the script to call plain `claude`) and a model your account
can use; leave it out if you have no advisor model.

---

## proof-check

**What.** `bin/proof-check PROOF.html` verifies that an HTML proof page will show its pictures in the control panel. The panel
renders HTML artifacts sandboxed (Content-Security-Policy `img-src https: data: blob:`) and serves at most 25 MB, so relative or
`file:` image paths show as broken pictures and a big file is cut off. The tool checks that every `<img>` (and CSS `url()`) is an
embedded, decodable `data:` image and that the file is under 15 MB; exit 1 if anything would not render.
`--embed` rewrites the file in place first: local images become JPEG data URIs (max 1600 px wide, quality 80 when Pillow is
installed; the original bytes otherwise), then it re-checks.

**Why.** A "proof of work" page full of broken screenshots proves nothing, and the failure is silent until the owner opens it.

**Where.** `bin/proof-check` (Python, standard library; Pillow optional). Linked into `~/.local/bin` by `setup.sh`.

**Check.** `python3 bin/proof-check some.html` (exit 0 = OK).

---

## Visual cards (attachments)

**What.** `attachments/` is a small message-ui project (the `@message-ui/*` npm packages; React rendered to a PNG).
`cards/` holds six templates: `metric-card`, `breakdown-card`, `rings-card`, `list-card`, `tasks-card`, `visual-gallery`;
`render.tsx` renders one with your data:

```sh
cd attachments
npx tsx render.tsx --list                          # each card, what it is for, its size and sample props (JSON)
npx tsx render.tsx metric-card --props '{"title":"CI pass rate","value":"97%","series":[90,94,97]}'
npx tsx render.tsx <card> --html --props @p.json   # the card as HTML (the panel draws cards live from this)
```

PNGs go to `~/jarvis-files/cards/<card>-<timestamp>.png` unless `--out` is given. The `vm` MCP tools `card_templates` and
`render_card` wrap this; in the panel a ` ```card ` block draws the real component, on Telegram (which cannot draw components)
JARVIS sends the PNG on a `MEDIA:/absolute/path` line. A new card is a `.tsx` file in `attachments/cards/` (a default-export
component with `PreviewProps` and `PreviewSize`; copy an existing one).

**Why.** A status table or a trend line reads faster as a picture on a phone, and the same component renders the same way
in both channels.

**Where.** `attachments/`; dependencies are installed by `setup.sh` (`npm ci`); the `vm` tools call it through `node` and
`node_modules/tsx`. `npm run dev` in `attachments/` is message-ui's live preview (port 3007).

**Check.** `cd attachments && npx tsx render.tsx --list`.

---

## Reply blocks: tables, buttons, cards, artifacts

**What.** `SOUL.md` teaches JARVIS fenced blocks that the panel renders richly and Telegram handles as described there:
` ```table ` (typed columns), ` ```buttons `, ` ```card `, ` ```chart ` and ` ```mermaid `, ` ```artifact ` (show a file an agent made, with
comment pins that come back as `[Comments on an artifact: <title>]`), ` ```proposal ` (a takeover card from the scout), and four
extra component blocks (` ```params `, ` ```ratemodel `, ` ```rates `, ` ```asset `). Images the owner sends arrive as
`[Attached image: <path>]` lines (saved in `~/jarvis-files/uploads`), other files as `[Attached file: <path>]`.

**Why.** Rendering lives in the panel (`web/ai-rich.js`, `assistant.js`), so the assistant only has to emit a block; the
instructions are behaviour, not code.

**Where.** `hermes/SOUL.md` (the format reference). **Check.** Ask JARVIS for "a table of my agents" in the panel.

---

## Contract tests

**What.** Two Python/Go layers keep this repo and the panel in step:

- `mcp/contract/run.sh <control-panel checkout>` copies a panel checkout to a temp dir (isolated home, fake zellij sockets,
  fake Hermes; nothing live is touched), adds this repo's `mcp/contract/*_test.go`, and runs
  `go test -run '^TestVMAgent' -v` against the panel's **real** agent API, driving the **real** `vm_mcp` tools and `bin/jarvis`
  through `mcp/contract_panel.py`. It asserts what the client sends and what the panel does with it (records on disk,
  verified plan states, refusals, owner-only routes not reachable, delivery of team news, ...). Options: `RACE=1`,
  `TIMEOUT=40m` (default 20m), `RUN=<regex>`; `LIVE_VM_AGENT_DIR=<deployed vm-agent>` adds two live-companion checks.
- `mcp/contract/hermes-smoke.sh <control-panel checkout> [--run]`: with `--run`, a real Hermes (fresh temp `HOME` and
  `HERMES_HOME`, only three `vm` tools exposed, 4 turns max) calls `thread_list` and `thread_update` against a disposable panel.
  It uses `ANTHROPIC_API_KEY` from the environment or a no-echo prompt, so it **bills a real model call**; without `--run` it only sets up
  and prints the command.

**Why.** The two repos change independently but share an HTTP contract (the `/agent/*` routes and the record rules). A client
test that fakes the panel proves nothing about the panel; this one uses the panel's own handlers.

**Where.** `mcp/contract/`, `mcp/contract_panel.py`. **Check.** `sh mcp/contract/run.sh ~/projects/control-panel` (needs `go`,
`uv`, `git`, `tar`; takes minutes; prints one `SCENARIO ok` line per scenario and `ok` at the end).

---

## Secrets handling

| Secret | Lives in | Mode | Read by |
|---|---|---|---|
| Model keys, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ALLOWED_USERS`, `API_SERVER_KEY` | `~/.hermes/.env` | 0600 | Hermes (and `setup.sh`, to compare values) |
| The Hermes API key shared with the panel | `~/.config/vm-agent/api-key` (dir 0700) | 0600 | the panel (`-ai-key-file`), `bin/jarvis` |
| The panel's agent API bearer token | `~/.config/control-panel/agent-token` | 0600, created by the panel on first start | `mcp/vm_mcp.py`, `bin/jarvis`, `bin/feature-doc`, `hermes/scripts/fleet_pings.py` |
| Google OAuth client and token | `~/.hermes/google_client_secret.json`, `~/.hermes/google_token.json` | Hermes-managed | Hermes' Google skill |

Rules the repo follows, and expects you to follow:

- **Nothing secret is ever in the repo** and `setup.sh` never asks for one. Keys are typed into the Hermes dashboard
  (panel, Assistant, Connections) or set with `hermes config set NAME value`; never paste one into a chat (`SOUL.md` also
  tells JARVIS never to print secrets).
- **Loopback only.** The Hermes API server binds `127.0.0.1:8642`, the dashboard `127.0.0.1:9119`, the panel's agent API
  `127.0.0.1:7778` (the panel refuses a non-loopback `-agent-addr`). The panel reaches the dashboard through its
  authenticated `/hermes/` proxy.
- **Allow-listed Telegram.** Only ids in `TELEGRAM_ALLOWED_USERS` can talk to the bot or tap its buttons.
- **Content is data.** Mail, web pages and documents JARVIS reads are untrusted; `SOUL.md` tells it never to follow instructions
  found in them. That is guidance, not enforcement: see the trade-off below.
- **`.gitignore`** excludes `run/` (logs), `__pycache__/`, `attachments/node_modules/` and `attachments/out/`.

The trade-off, stated plainly: `setup.sh` sets `approvals.mode off`. JARVIS has a full shell with no approval prompts (Hermes'
hardline blocklist of catastrophic commands still applies), on purpose, because Telegram is meant to be the main way to drive the
VM. That is reasonable for one trusted owner on a dev VM; it is not something to expose to other users. If prompt injection
through email worries you, set `approvals.mode smart` or run mail-reading jobs with a restricted toolset.

---

## Mechanisms that live in the panel

These are in the control-panel repo but shape how JARVIS behaves; details in [THREADS.md](THREADS.md).

- **Thread turns with `LEAD.md`** (`threads.go`): every lead turn carries the playbook and the `[Feature record]` block.
- **Team news outbox** (`teamdeliver.go`): durable hand-off of "your agents finished/asked/stalled" notes to the lead.
- **Supervisor** (`featsup.go`): approval dialogs, exited agents, quiet teams, missed checkpoints, tickets; never kills.
- **Record writer and verification** (`featdoc.go`, `featverify.go`): revision-checked writes, receipts, the done gate.
- **Scout** (`scout.go`): hourly look for stalled hand-started sessions; a takeover proposal at most every 12 h.
- **Notes** (`notes.go`): the notification list that fleet pings and `recent_events` read.
