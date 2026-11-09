# JARVIS (vm-agent)

A personal assistant for a development VM, built on [Hermes Agent](https://github.com/NousResearch/hermes-agent).
You talk to it on Telegram or in your control panel; it has a shell on the VM, runs a fleet of
Claude Code and Codex agents in zellij, watches the machine, and handles email, calendar and whatever else you connect
over MCP. Big pieces of work get a **thread**: a fresh JARVIS that acts as product lead for one feature, runs its own team
of agents on its own git branch, and asks you before anything lands on `stage`.

This repo is the part that is *yours to change*: the persona and playbooks (`hermes/SOUL.md`, `hermes/LEAD.md`, skills), the
`vm` MCP server that gives Hermes its tools, the helper commands agents run in panes, the Telegram button plugin, and the
scripts that install, configure and supervise it all. Hermes itself is installed by `setup.sh`, and the web UI and the
machine API live in the companion `control-panel` repo.

## What you get

- **One assistant, two doors.** Telegram (text, voice notes, tap-to-answer buttons) and the panel's Assistant view are two
  channels of the same agent with shared memory.
- **A supervised fleet.** `fleet_status`, `pane_chat`, `pane_send`, `pane_answer`, `open_terminal` ... let it see every Claude
  or Codex agent, answer their dialogs, start new ones with a first prompt, and tell you on Telegram when one finishes, asks
  or stalls (and let you answer from the phone).
- **Threads.** "Add CSV export to the reports page" opens a thread: a lead plans it, spawns a team (researcher, implementer,
  tester ...), keeps a feature record whose steps are verified by evidence, and ships one squashed commit after your yes.
  [docs/THREADS.md](docs/THREADS.md) explains the whole framework.
- **Agents that can call home.** `jarvis "PR is green"` from any pane reaches JARVIS (or the pane's lead) with one conversation
  per pane; `feature-doc` writes the feature record safely; `notify-owner` escalates a real impasse.
- **Guard rails.** Owner-only approvals in the panel, a done gate, a merge guard hook, rate-limited escalation.
  See [docs/MECHANISMS.md](docs/MECHANISMS.md).
- **Rich replies.** Tables, buttons, charts, mermaid diagrams, live visual cards and artifact review with comment pins in the
  panel; compact cards and buttons on Telegram.

## How it fits together

```
Telegram ──────►  Hermes gateway  ◄── Hermes API server 127.0.0.1:8642 ◄── control panel: Assistant view
 (DM, voice,       │  SOUL.md, skills, memory, cron                              (panel.example.com, behind
  buttons)         │                                                              Cloudflare Access)
                   │ MCP (stdio)
                   ▼
            mcp/vm_mcp.py ──HTTP + bearer token──► control panel agent API 127.0.0.1:7778
            (37 "vm" tools)                           │ drives zellij, owns threads, feature records, notes
                                                      ▼
                                     zellij fleet: Claude Code / Codex agents
                                     (a thread's team = one session, a worktree per agent)
                                          │  jarvis · feature · feature-doc · notify-owner   (bin/, from the panes)
                                          └────────────────────────────► back to Hermes / the panel

Hermes dashboard 127.0.0.1:9119 (keys, models, MCP, skills, cron) ── proxied by the panel at /hermes/
```

Details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Requirements

Everything runs as a normal user (no root, no systemd). On the VM:

| Needed | For | Notes |
|---|---|---|
| Linux, `bash`, `curl`, `git`, `python3` (3.11+), `tar` | everything | the helper scripts use only the Python standard library |
| [`uv`](https://docs.astral.sh/uv/) on `PATH` | the `vm` MCP server (`uv run --script`, needs `mcp>=2.3,<3`) | `setup.sh` fails without it. Hermes' installer provisions its own pinned uv for Hermes, so install uv yourself. |
| `node` and `npm` | the visual cards (`attachments/`, `npm ci` at setup) | |
| [Hermes Agent](https://github.com/NousResearch/hermes-agent) | the agent | **installed by `setup.sh`** if `hermes` is not on `PATH` (upstream installer). Developed against v0.21.x. |
| The `control-panel` repo at `~/projects/control-panel`, built and running | every fleet, thread and feature tool; the Assistant view; the `/hermes/` dashboard portal | Follow its README (it needs Go to build, and a Cloudflare tunnel plus Access app for remote use). It creates `~/.config/control-panel/agent-token` on first start. Plain Telegram chat works without it; the `vm` tools answer "control panel not reachable". |
| [`zellij`](https://zellij.dev) | the panel drives it; agents run in its panes | `bin/jarvis`, `notify-owner` and the guard hook read `ZELLIJ_SESSION_NAME` / `ZELLIJ_PANE_ID` |
| Claude Code (`claude`), optionally Codex (`codex`) | the agents JARVIS starts | the panel types the value of its `CP_CLAUDE_CMD` (default `claude`) or **`codex`** into a new pane, so those commands must resolve in the shell zellij starts. To start Claude without permission prompts (the panel already surfaces on-screen questions), set `export CP_CLAUDE_CMD="claude --dangerously-skip-permissions"` in the panel's `panel.env`; without it, Claude asks for permissions as normal. |
| A model provider key | the brain | OpenRouter, Anthropic, OpenAI, ... or a login Hermes supports (`hermes model`) |
| A Telegram bot token and your numeric user id | the phone channel | from @BotFather and @userinfobot |

Optional: a Google OAuth client (Gmail and Calendar), a `stage` branch in your repos (`feature ship` squashes into it), `gh`
(the panel uses it for PR and CI views), `go` (to run the contract tests).

Clone both repos under `~/projects` (the docs and `SOUL.md` use those paths; the scripts find their own directory, so the
checkout can live anywhere):

```sh
mkdir -p ~/projects && cd ~/projects
git clone https://github.com/roomss-ltd/control-panel control-panel
git clone https://github.com/roomss-ltd/vm-agent vm-agent
```

## Quick start

1. **Start the control panel** from `~/projects/control-panel` (see its README: copy `panel.env.example` to `panel.env`, then
   `./run-panel.sh` in a terminal tab or `./ensure-panel.sh` from your shell profile).
2. **Install and start the assistant:**

   ```sh
   cd ~/projects/vm-agent
   ./run-agent.sh
   ```

   `run-agent.sh` runs `setup.sh` (installs Hermes if needed, configures `~/.hermes` from this repo), then keeps the Hermes
   gateway and dashboard alive in the foreground. Leave it in a terminal tab, for example a zellij tab.
3. **Make it survive logins and reboots.** Add one line to your shell profile (`~/.zshrc` or `~/.bashrc`):

   ```sh
   ~/projects/vm-agent/ensure-agent.sh
   ```

   It starts `run-agent.sh` in the background (log in `run/agent.log`) when no gateway is running, and does nothing otherwise.
4. **Configure it** (next section), then say hello on Telegram.

## First-time configuration

Everything is entered in the panel under **Assistant, Connections** (the Hermes dashboard, proxied at `/hermes/`). Secrets are
stored by Hermes in `~/.hermes/.env` (mode 600); never paste one into a chat. Without the panel, the same dashboard is
`http://127.0.0.1:9119` on the VM (for example through an SSH port forward), and `hermes config set NAME value` works from a
shell (mind your shell history for secrets).

1. **Model.** Under Keys add `OPENROUTER_API_KEY`, or `ANTHROPIC_API_KEY` / `OPENAI_API_KEY`; then pick a default under Models
   (or run `hermes model`). Switch per task with `/model` in any chat.
2. **Telegram.** Under Keys set `TELEGRAM_BOT_TOKEN` (from @BotFather) and `TELEGRAM_ALLOWED_USERS` (your numeric id, from
   @userinfobot; comma-separate more). Then run `./setup.sh` once more (or restart `run-agent.sh`): it makes your DM the
   *home channel* (`TELEGRAM_HOME_CHANNEL`), which is where fleet pings go. Restart the gateway and message the bot.
3. **Google** (Gmail, Calendar). Upload the OAuth client JSON (type Desktop app, consent screen in production) and press
   **Connect**. JARVIS sends a sign-in link; paste back the URL the browser lands on.
4. **More.** MCP servers (from the catalog, or a URL/command), skills and scheduled jobs are in the same dashboard.

Make it yours: edit `hermes/SOUL.md` (who it is and how it writes), `stt.prompt` in `setup.sh` (names your voice notes should
get right) and the skill in `hermes/skills/vm-agent/vm-fleet/` (house rules for the fleet).

**Optional extra: the merge guard.** `setup.sh` links `notify-owner`, `guard-no-merge-hook`, `proof-check` and
`fable-advise` into `~/.local/bin`, but it does not register the hook. To turn the guard on, register it in
`~/.claude/settings.json` (PreToolUse, matcher `Bash`); see
[docs/MECHANISMS.md](docs/MECHANISMS.md#guard-no-merge-hook).

## Running and restarting

- `run-agent.sh` (foreground): setup, then gateway (Telegram, the API server on `127.0.0.1:8642`, cron) and dashboard
  (`127.0.0.1:9119`), each restarted if it exits. Ctrl-C stops them for good.
- `ensure-agent.sh` (from your shell profile): starts `run-agent.sh` in the background if no gateway runs; log in `run/agent.log`.
- After changing `SOUL.md`, `LEAD.md`, a skill, the MCP server or the plugin: restart the gateway (**Restart Gateway** in the
  dashboard, or kill it and the loop relaunches it). After changing `setup.sh`: run `./setup.sh`.
- `hermes status` shows whether the gateway is up and which platforms are connected; `hermes cron list` shows the `fleet-pings` job.

## Using it

Message it like a colleague. Things it is set up for: "what is running?", "start an agent on `<repo>` to `<task>`", "tell the
auth agent to rebase first", answering a Fleet ping by replying to it, "open a thread for `<feature>`", sending it a photo,
spreadsheet or voice note. Agents reach it with `jarvis "..."` (see [docs/MECHANISMS.md](docs/MECHANISMS.md#jarvis-cli-one-conversation-per-pane)).

## Repo layout

| Path | What it is |
|---|---|
| `setup.sh` | idempotent install and configuration of Hermes from this repo |
| `run-agent.sh`, `ensure-agent.sh` | foreground supervisor; login-time "start it if it is not running" |
| `hermes/SOUL.md` | persona and rules of every JARVIS (Main and leads); reply formats |
| `hermes/LEAD.md` | the product-lead playbook the panel sends with every thread turn |
| `hermes/skills/vm-agent/vm-fleet/` | fleet know-how skill (tools, conventions) |
| `hermes/plugins/vm-telegram/` | Hermes plugin: ` ```buttons ` blocks become Telegram inline buttons |
| `hermes/scripts/fleet_pings.py` | cron script: panel notifications to Telegram |
| `mcp/vm_mcp.py` | the `vm` MCP server (37 tools) over the panel's agent API; `mcp/test_vm_mcp.py` is its self-test |
| `mcp/contract/`, `mcp/contract_panel.py` | cross-repo contract tests against the control panel's real handlers |
| `bin/jarvis` | agent to JARVIS (or to the pane's lead); one Hermes conversation per pane |
| `bin/feature` | one branch per thread, a worktree per agent, one squashed commit into `stage` |
| `bin/feature-doc` | read and write a feature record through the panel, against a revision |
| `bin/notify-owner` | rate-limited Telegram escalation for an impasse |
| `bin/guard-no-merge-hook` | Claude Code hook: muted panes may not merge or push to `stage`/`main` |
| `bin/proof-check` | will this PROOF.html show its images in the panel? (`--embed` fixes it) |
| `bin/fable-advise` | optional read-only "ask a stronger model" helper (needs a `teamclaude` launcher; see its header) |
| `bin/test_*.py` | self-tests for the `bin/` tools |
| `claude/skills/jarvis/` | Claude Code skill: when and how an agent talks to JARVIS (linked into `~/.claude/skills`) |
| `attachments/` | message-ui project: visual card templates and `render.tsx` |
| `docs/` | [ARCHITECTURE](docs/ARCHITECTURE.md), [THREADS](docs/THREADS.md), [MECHANISMS](docs/MECHANISMS.md) |
| `AGENTS.md` | guide for coding agents working in this repo |
| `LICENSE` | MIT |

## Adding a VM tool

Add an `@mcp.tool()` function to `mcp/vm_mcp.py`. It can call the panel with `panel(method, path, body)` or do the work itself.
Hermes picks it up on the next gateway restart. Run the self-test (below), and if the tool touches a panel route, the
contract tests too.

## Testing

```sh
python3 bin/test_jarvis.py                                   # bin/jarvis against stand-in Hermes and panel
python3 bin/test_feature.py                                  # bin/feature on a scratch git repo
python3 bin/test_feature_doc.py                              # bin/feature-doc against a stand-in panel
uv run --with "mcp>=2.3,<3" python mcp/test_vm_mcp.py        # vm_mcp formatting and tool routing
# the Telegram plugin needs a Hermes virtualenv that has python-telegram-bot:
~/.hermes/installs/*/environments/<env>/venv/bin/python hermes/plugins/vm-telegram/test_vm_telegram.py
# the contract with the real control panel (needs go; minutes; touches nothing live):
sh mcp/contract/run.sh ~/projects/control-panel
```

Each self-test prints `ok`. The contract run prints a `SCENARIO ok` line per scenario and Go's `ok` at the end. More in
[docs/MECHANISMS.md](docs/MECHANISMS.md#contract-tests) and [AGENTS.md](AGENTS.md).

## A word on trust

This is built for **one trusted owner on a development VM**. `setup.sh` turns Hermes' approval prompts off (its hardline
blocklist of catastrophic commands still applies), JARVIS has a full shell, and it reads mail and web pages that may contain
hostile instructions. `SOUL.md` tells it to treat that content as data, which is guidance, not enforcement. Telegram is
allow-listed to your user ids, every service listens on loopback, and the only public door is the panel behind Cloudflare
Access. Read [Secrets handling](docs/MECHANISMS.md#secrets-handling) before putting it anywhere that matters.

## Documentation

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): components, ports, files, how a message travels, the agent API contract
- [docs/THREADS.md](docs/THREADS.md): Main JARVIS, threads, product leads, teams, the feature record, shipping
- [docs/MECHANISMS.md](docs/MECHANISMS.md): every mechanism, what/why/where/how to check
- [AGENTS.md](AGENTS.md): conventions for Claude Code, Codex and other agents editing this repo

## License

MIT.
