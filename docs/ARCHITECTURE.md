# Architecture

JARVIS is a [Hermes Agent](https://github.com/NousResearch/hermes-agent) installation configured by this repo, plus a
small MCP server that lets it drive the control panel. This page explains the components, the ports, the files and how a
message travels. For the threads framework see [THREADS.md](THREADS.md); for the individual mechanisms see
[MECHANISMS.md](MECHANISMS.md).

## The pieces

```
 you ── Telegram ──┐                           ┌── browser: panel.example.com (Cloudflare Access)
 (text, voice,     │                           │   Assistant view, Terminals, feature pages, /hermes/
  buttons)         ▼                           ▼
   ┌───────────────────────────┐      ┌─────────────────────────────────────────┐
   │ Hermes gateway            │      │ control panel (Go, one binary)          │
   │  Telegram adapter         │      │  UI on :7777                            │
   │  + vm-telegram plugin     │◄─────┤  talks to Hermes' API server            │
   │  agent loop: SOUL.md,     │ 8642 │  127.0.0.1:8642 (key file)              │
   │  skills, memory           │      │                                         │
   │  cron: fleet-pings (1m)   │      │  agent API 127.0.0.1:7778 (bearer token)│
   │  MCP client ─ stdio ─┐    │      │  threads, feature records, notes,       │
   └──────────────────────┼────┘      │  supervisor, scout                      │
                          ▼           │                                         │
              mcp/vm_mcp.py ──────────►                                         │
              (the "vm" tools)  HTTP  └───┬────────────────────────┬────────────┘
                                          │ drives zellij          │ proxies /hermes/
                                          ▼                        ▼
   ┌─ zellij sessions ───────────────────────────┐     Hermes dashboard 127.0.0.1:9119
   │ dev: Claude / Codex agents, shells          │     (keys, models, MCP servers,
   │ <thread-id>: a thread's team, a tab per     │      skills, cron, logs)
   │ role, each agent in its own git worktree    │
   └──────────────────────┬──────────────────────┘
                          │ run in those panes: jarvis, feature, feature-doc, notify-owner
                          └──► back to the Hermes API server (jarvis) and the panel (feature-doc)
```

| Component | Where | What it does |
|---|---|---|
| **Hermes gateway** | `hermes gateway run --external-supervisor`, started by `run-agent.sh` | The agent. Owns the model calls, the Telegram connection, memory, cron, the MCP client and the API server. All state is in `~/.hermes/`. |
| **Hermes API server** | `127.0.0.1:8642` (`API_SERVER_*`, key in `~/.config/vm-agent/api-key`) | How the panel and `bin/jarvis` start and read conversations: `POST /api/sessions`, `/api/sessions/<id>/chat` and `/chat/stream` (SSE), `GET /api/sessions/<id>/messages`. |
| **Hermes dashboard** | `127.0.0.1:9119` (`hermes dashboard`) | Hermes' own web app: model keys, models, channels, MCP servers, skills, cron jobs, logs. Reached through the panel at `/hermes/`. |
| **`vm` MCP server** | `mcp/vm_mcp.py`, run by Hermes over stdio as `uv run --quiet --script` | 37 tools: fleet, threads and feature records (each turns into a call to the panel's agent API), plus `card_templates` and `render_card`. |
| **Control panel** | the `control-panel` repo; main listener `:7777` behind a Cloudflare tunnel and Cloudflare Access; **agent API on `127.0.0.1:7778`** | The human UI, and the machine API this repo calls. Owns zellij, threads, feature records, notes, the supervisor and the scout. |
| **zellij fleet** | zellij sessions on the VM | Where the Claude Code and Codex agents actually run. JARVIS never calls zellij directly; the panel does, on its behalf. |
| **`bin/` helpers** | linked into `~/.local/bin` | Run inside panes: `jarvis`, `feature`, `feature-doc`, `notify-owner`, `proof-check`, `guard-no-merge-hook`. |
| **Behaviour files** | `hermes/SOUL.md`, `hermes/LEAD.md`, `hermes/skills/`, `claude/skills/jarvis/` | The persona, the lead playbook, fleet know-how, and what Claude Code agents are told about JARVIS. Prompts, not code. |

## Ports and files at a glance

| What | Value |
|---|---|
| Hermes API server | `127.0.0.1:8642`, bearer key = contents of `~/.config/vm-agent/api-key` |
| Hermes dashboard | `127.0.0.1:9119`, proxied by the panel at `/hermes/` (plus `/assets/`, `/fonts/`, `/fonts-terminal/`, `/ds-assets/`, which its lazy chunks request unprefixed) |
| Panel agent API | `127.0.0.1:7778` (`-agent-addr`; the panel refuses a non-loopback address; empty turns it off), bearer token in `~/.config/control-panel/agent-token` |
| Panel UI | `:7777` (loopback; published through the Cloudflare tunnel) |
| Hermes state | `~/.hermes/` (`.env` secrets, `config.yaml`, sessions DB, memory, `plugins/`, `scripts/`, `SOUL.md` and `LEAD.md` symlinks) |
| This repo's state | `~/.local/state/vm-agent/` (`pings.json`, `tg-buttons.json`, `escalations/`, `agent-replies/`, `unsent/`, `muted-outbox/`), `run/agent.log` in the checkout |
| Panel state | `~/.local/state/control-panel/` (`threads.json`, `notes.json`, `scout.json`, `assistant.json`, ...) |
| What JARVIS and its leads write for you | `~/jarvis-files/` (`uploads/`, `cards/`, `threads/<id>/`, `features/<session>/`), worktrees in `~/worktrees/<id>/` |

Override knobs: `PANEL_AGENT_URL` (default `http://127.0.0.1:7778`), `PANEL_AGENT_TOKEN_FILE`, `JARVIS_URL` (default
`http://127.0.0.1:8642`), `FEATURE_ROOT` (worktrees), `VM_AGENT_PING_KINDS`, `FABLE_TIMEOUT`.

## Hermes sessions

Hermes keeps one session per conversation. The names tell you who is talking:

| Session | Who | Notes |
|---|---|---|
| `panel` | the owner, in the panel's Assistant view | created by the panel (API-server source) |
| the Telegram DM session | the owner, on Telegram | never resets; one continuous chat |
| `thread-<id>` | a thread's lead | driven by the panel; every turn carries `LEAD.md` |
| `agent-<session>-<pane>` (`agent-shell` outside zellij) | an agent's own conversation with JARVIS | created by `bin/jarvis` |

Main JARVIS is the panel session plus the Telegram DM and the per-pane sessions: one identity, one shared memory. The
panel's Assistant view merges the panel and Telegram conversations into one chat in time order, each message labelled with
its channel, and sends what you type to the `panel` session. A lead has the same `SOUL.md` and memory but its own
conversation.

## How a message travels

**You on Telegram.** Telegram adapter, agent loop (model plus tools), reply. A reply ending in a ` ```buttons ` block
passes through the `vm-telegram` plugin, which turns the block into inline buttons. A voice note is transcribed locally first.
When the agent needs the fleet it calls a `vm` tool: `vm_mcp.py` makes an HTTP call to the panel's agent API with the bearer
token, and the panel reads or types into the zellij pane.

**You in the panel.** The Assistant view posts to `/api/ai/...` on the panel, which calls the Hermes API server
(`/api/sessions/panel/chat/stream`) and streams the turn back. Tool calls and reasoning show on a rail above the answer.
The reply is the same agent with the same memory; only the channel differs.

**An agent in a pane.** `jarvis "..."` posts to the Hermes API server (own session per pane) or, in a thread's team, to the
panel, which hands the message to the lead. JARVIS answers on the same path, or later by typing into the pane.

**The VM talking to you.** The panel's notifier records notes (agent finished or asking or stalled, CI failures, security
checks, disk, memory, thread needs-you). The `fleet-pings` cron job reads `GET /agent/notes` every minute and prints the
new ones; Hermes delivers that text to the Telegram home chat and mirrors it into the DM session. Your reply to a ping
arrives as `[Replying to: "..."] text` and JARVIS acts on the address in the quote.

**A thread turn.** The panel (not the lead) starts it: it sends the message to `thread-<id>` with `instructions` =
`~/.hermes/LEAD.md` filled in with the thread's record plus a `[Feature record]` block, queues messages behind a running turn,
and keeps delivering the team's news until the lead has durably received it. See [THREADS.md](THREADS.md).

**Settings and secrets.** The Connections portal is the Hermes dashboard. What you type there lands in `~/.hermes/.env`
(mode 0600) or `config.yaml`; the repo never sees it. The panel attaches the dashboard's session token itself, so you sign in
once, to the panel.

### The dashboard proxy

The panel is the front door for the dashboard: `hermesProxy` forwards `/hermes/*` (and the four unprefixed asset paths) to
`127.0.0.1:9119`, strips the `/hermes` prefix and sets `X-Forwarded-Prefix: /hermes`, drops `Origin`, `Referer`, the panel's own
cookies and the Cloudflare Access JWT (the panel's credentials stay with the panel), and adds the dashboard's own session token
(read from its loopback page, re-read after a 401 when the dashboard restarted). Hermes sees a loopback client, so its Host and
Origin guards pass. If the dashboard is not running the panel says so (it starts with `run-agent.sh`).

## The agent API contract

The panel's agent API is the only interface between this repo and the panel. Everything is JSON over HTTP on loopback, with
`Authorization: Bearer <token>`; every write is audited by the panel with the client shown as "agent". The routes, and who here calls them:

| Route | Called by |
|---|---|
| `GET /agent/snapshot?view=term\|res\|ci\|sec` | `fleet_status` (term), `vm_status` (res, ci, sec) |
| `GET /agent/pane/screen`, `GET /agent/pane/chat`, `GET /agent/pane/files` | `pane_screen`, `pane_answer`, `open_terminal`, fleet pings (dialog options); `pane_chat`; `pane_files` |
| `POST /agent/pane/input` | `pane_send`, `pane_answer`, `pane_keys`, `open_terminal`, `bin/jarvis` (late answers) |
| `POST /agent/term/new`, `POST /agent/term/close` | `open_terminal`, `close_terminal` |
| `GET /agent/notes` | `recent_events`, `fleet_pings.py` |
| `GET /agent/threads` | `thread_list`, `bin/jarvis`, `fleet_pings.py` |
| `POST /agent/thread/open`, `/send`, `/update` | `thread_open`; `thread_send`, `bin/jarvis`; `thread_update` |
| `GET /agent/feature`, `POST /agent/feature/doc`, `POST /agent/feature/log` | `feature_record`, `get_*`, `create_*`/`update_*`, `ask_owner`, `add_*`, `report_feature_progress`, `feature_log`; `bin/feature-doc` |
| `GET /agent/feature/team`, `POST /agent/feature/assign`, `GET /agent/feature/events` | `get_feature_team`/`get_team_member`; `assign_feature_job`; `get_feature_events` |
| `POST /agent/scout`, `POST /agent/pane/codexmodel` | not called from this repo |

What is deliberately **not** on this API: approving a step's check, marking a step verified, accepting outcomes, answering an
owner ticket, releasing a hold, promoting a requirements draft. Those are the owner's page routes (`/api/term/feature/...`),
behind the panel's login. A contract test asserts that an agent cannot reach them.

Because the panel and this repo change independently, the contract is tested against the panel's real handlers:
`mcp/contract/run.sh <control-panel checkout>` (see [MECHANISMS.md](MECHANISMS.md#contract-tests)). Change a tool or a route on one
side, run it on both.

## Supervision

```
ensure-agent.sh  (shell profile, every login)  ──starts if no gateway runs──►  run-agent.sh
run-agent.sh ── setup.sh                         (idempotent configuration, then:)
             ├─ loop: hermes dashboard  (127.0.0.1:9119)     restart 2 s after exit
             └─ loop: hermes gateway run --external-supervisor --replace
                                                   exit 75 → relaunch now; other exit → relaunch after 2 s
```

The panel has the same shape on its side (`ensure-panel.sh`, `run-panel.sh`). Neither needs systemd or root.

## Design decisions worth knowing

- **One owner, one identity, on purpose.** Telegram is allow-listed to the owner's user ids; there is no multi-user mode.
- **Full access, no approval prompts.** `approvals.mode off` (Hermes' hardline blocklist still applies). The trade-off is
  discussed in [MECHANISMS.md](MECHANISMS.md#secrets-handling).
- **Behaviour is prompts.** `SOUL.md`, `LEAD.md` and the skills are the product; changing how JARVIS behaves is usually an edit
  to a markdown file, not to code. See [../AGENTS.md](../AGENTS.md).
- **The panel owns state and judgement; JARVIS owns conversation.** Threads, records, receipts and the done gate live in the
  panel so no agent (including a lead) can mark its own work verified.
- **Loopback everywhere.** Nothing in this repo listens on a public interface; the only public door is the panel behind
  Cloudflare Access.
- **Pings through cron, not hooks.** The fleet-ping job is a no-agent cron script rather than a push from the panel, at the
  price of up to a minute of latency. A Hermes webhook route would remove the delay if you need it.
- **The panel's failed turns live in its memory** (a provider error Hermes did not store), so a panel restart drops them.
