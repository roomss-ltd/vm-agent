# AGENTS.md

Guide for coding agents (Claude Code, Codex, ...) working **in this repository**. Humans: start with the
[README](README.md). Before changing behaviour, read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); before touching threads,
leads or feature records, read [docs/THREADS.md](docs/THREADS.md).

## What this repo is

JARVIS: a personal assistant on a dev VM, built on Hermes Agent. This repo holds what Hermes is *configured with*: the persona and
playbooks (markdown), an MCP server (Python), helper commands for terminal panes (Python and shell), a Telegram plugin, visual
card templates (TypeScript), and install/supervise scripts (shell). It talks to the companion **control-panel** repo only over
HTTP, through the panel's loopback agent API (`127.0.0.1:7778`).

**Prompts are behaviour.** `hermes/SOUL.md`, `hermes/LEAD.md`, `hermes/skills/**/SKILL.md` and
`claude/skills/jarvis/SKILL.md` are sent to models; editing them changes what JARVIS does as surely as editing code, and they
have no compiler. Treat them like code: small diffs, a reason for each line, and check them against the tools they name.

## Layout

| Path | What it is | Change it when | How to test |
|---|---|---|---|
| `hermes/SOUL.md` | persona, safety rules, reply formats (`table`, `buttons`, `card`, `artifact`, `proposal` ...), how Main handles threads and fleet pings | JARVIS should behave or write differently | read it against the panel renderers (`web/ai-rich.js`, `assistant.js` in control-panel); try it in a chat |
| `hermes/LEAD.md` | product-lead playbook; the panel sends it with every thread turn after replacing `{id} {title} {team} {repo} {branch} {status} {note}` | the thread workflow changes | `docs/THREADS.md` must still be true; contract tests if a tool argument changes |
| `hermes/skills/vm-agent/vm-fleet/SKILL.md` | fleet know-how loaded by Hermes (`skills.external_dirs`) | a standing convention for driving agents | try it in a chat |
| `claude/skills/jarvis/SKILL.md` | what Claude Code agents are told about the `jarvis` command and a thread's team | the agent-side protocol changes | keep consistent with `bin/jarvis`, `LEAD.md` |
| `mcp/vm_mcp.py` | the `vm` MCP server: one `@mcp.tool()` per tool, `panel()` helper for the agent API | a tool is added or changed | `uv run --with "mcp>=2.3,<3" python mcp/test_vm_mcp.py`; `mcp/contract/run.sh` for panel routes |
| `mcp/contract/`, `mcp/contract_panel.py` | cross-repo contract tests (Go files that run inside a copy of the panel; a Python driver) | a route or a rule shared with the panel changes | `sh mcp/contract/run.sh <control-panel checkout>` |
| `bin/jarvis` | agent to JARVIS / its lead | the pane protocol changes | `python3 bin/test_jarvis.py` |
| `bin/feature` | branch, worktree and squash discipline (plain git) | the git workflow changes | `python3 bin/test_feature.py` |
| `bin/feature-doc` | pane-side feature-record client | the record routes change | `python3 bin/test_feature_doc.py` |
| `bin/notify-owner`, `bin/guard-no-merge-hook`, `bin/proof-check`, `bin/fable-advise` | small guard and helper commands | rarely | run them by hand with throw-away `HOME` (see MECHANISMS.md) |
| `hermes/plugins/vm-telegram/` | Hermes plugin for Telegram inline buttons | button behaviour changes | `test_vm_telegram.py` with a Hermes venv that has `telegram` |
| `hermes/scripts/fleet_pings.py` | cron script: panel notes to Telegram | ping format or kinds change | read it; do not run it live (it advances the cursor) |
| `attachments/` | message-ui project: `cards/*.tsx`, `render.tsx` | new or changed card | `cd attachments && npx tsx render.tsx --list`, then render one |
| `setup.sh`, `run-agent.sh`, `ensure-agent.sh` | install, configure, supervise | Hermes configuration changes | `bash -n setup.sh`; then run `./setup.sh` twice on a disposable VM or container (it configures the real `~/.hermes`) and check the second run changes nothing |
| `docs/`, `README.md` | documentation | behaviour or layout changed | keep tables and commands true |

## Conventions

**Python** (`bin/*`, `mcp/*`, plugin, cron script)
- `bin/` tools use only the standard library and must stay that way (they run in arbitrary panes). `vm_mcp.py` declares its one
  dependency in a PEP 723 header (`mcp>=2.3,<3`) and runs under `uv run --script`.
- A module docstring and a docstring on every public function, in plain English: what and why, not history. A tool's docstring in
  `vm_mcp.py` *is the tool description the model reads*: write it for the model, and keep it exact about arguments and refusals.
- Reuse what is there: `panel()` / `PanelError` / `q()` in `vm_mcp.py`; the loopback opener (`ProxyHandler({})`) so calls never
  go through an HTTPS proxy; `secret()` for token files.
- Tool output is compact text (cheap in tokens), not raw JSON, except where JSON is the point (`vm_status`). Errors from the
  panel become `PanelError` with the panel's message; do not swallow them.
- Self-tests are plain `assert` scripts that print `ok`. No framework, no fixtures. A tool that formats output gets its formatter
  unit-tested with a literal input; a tool that calls the panel is covered by the contract tests.

**Shell**
- `set -euo pipefail` where the script is non-interactive and expects it (`setup.sh`); quote variables.
- Scripts find their own directory (`cd "$(dirname "$0")"`); never hardcode a home directory. Use `$HOME`, `~`, `Path.home()` or
  `os.path.expanduser("~")`. State goes under `~/.local/state/vm-agent/`, config under `~/.config/vm-agent/`.
- `setup.sh` is **idempotent**: every step checks before it changes (`hc key value` sets a Hermes config key only if it differs,
  `ln -sfn`, "create if missing"). A new step must keep a second run a no-op. Hermes configuration goes through `hermes config set`
  / the `hc` helper, never by editing `~/.hermes` files directly.

**Markdown behaviour files**
- Write for the model and for the busy owner reading its output. `SOUL.md` and `LEAD.md` are sent every turn: shorter is cheaper.
- Tool names, argument names and status values quoted in `SOUL.md`, `LEAD.md` and the skills must match `mcp/vm_mcp.py` and the
  panel. If you rename or re-shape a tool, update every prompt that mentions it, `docs/THREADS.md` and the contract tests in the
  same change.
- Never put real names, companies, hostnames, emails, ids or paths of a particular person in them. Use "the owner",
  `panel.example.com`, `example-org/example-repo`. The owner's house rules belong in the owner's own copy, not in the repo.

**Go and TypeScript**
- `mcp/contract/*_test.go` are `package main` files that only compile inside a control-panel checkout (they use its test helpers
  such as `newSupEnv`); `go test` in this directory does not work, use `run.sh`. Keep them `gofmt`-clean.
- `attachments/cards/*.tsx`: the first line of a card is a `// ` comment saying what it is for (`render.tsx --list` prints it);
  default-export a component with static `PreviewProps` and `PreviewSize`; inline styles from `../theme`.

## The cross-repo contract with control-panel

The panel's agent API (`agent.go` in control-panel; list in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#the-agent-api-contract))
is the only interface. Rules:

1. **Both sides change together or not at all.** If you change what a tool sends, what `bin/jarvis` or `bin/feature-doc` expects,
   or a `LEAD.md` rule about records, the matching panel change belongs in the control-panel repo. Say so in your hand-off.
2. **Run the contract tests when you touch a route or a record rule:**
   `sh mcp/contract/run.sh ~/projects/control-panel`. It copies the panel to a temp dir (isolated home, fake zellij and Hermes), so
   nothing live is touched, but it takes minutes. `RUN='^TestVMAgentWriter'` runs one test; `RACE=1` adds the race detector.
3. **Never add an owner-only action to the agent surface.** Approving a check, marking a step verified, accepting outcomes,
   answering an owner ticket, releasing a hold and promoting a draft are the owner's page routes. A tool for them would break the
   guarantee the done gate rests on (a contract test asserts agents cannot reach them).
4. **Do not teach agents to bypass the gates**: ticking a box is a claim; only a receipt or the owner verifies; a lead does not
   tick its team's boxes; `feature ship ... --owner-approved` follows the owner's yes.

## Do

- Read the file and its tests before editing; run the self-test for the part you changed, and the contract tests for panel-facing
  changes. Say which you ran and what they showed.
- Keep changes small and the repo tables (README layout, MECHANISMS) true. If you add a script or a tool, document it where a
  reader would look.
- Develop against a sandbox, not the live assistant. The tools honour overrides: `PANEL_AGENT_URL`, `PANEL_AGENT_TOKEN_FILE`,
  `JARVIS_URL`, `FEATURE_ROOT`, `HOME` (point it at a temp dir). The tests already do this.
- When a behaviour question is really a prompt question, change the markdown, not the code.
- Keep `.gitignore` honest: `run/`, `__pycache__/`, `attachments/node_modules/` and `attachments/out/` are never committed.

## Don't

- **Never commit secrets** or anything from `~/.hermes/.env`, `~/.config/vm-agent/api-key`, `~/.config/control-panel/agent-token`,
  Google OAuth files, `run/agent.log`, or a state directory. Do not print them either. Model keys, the Telegram bot token and
  allowed user ids live in `~/.hermes/.env` (mode 600), set through the Hermes dashboard or `hermes config set`.
- Do not hardcode a user, home path, hostname, domain, id or company. Do not leave personal examples in tests or comments.
- Do not edit `~/.hermes/*` or the symlinks `setup.sh` makes by hand; change the source here and let `setup.sh` apply it.
- Do not restart or reconfigure the live gateway to test an idea; use a scratch `HOME`. Do not run
  `mcp/contract/hermes-smoke.sh ... --run`: it makes a real, billed model call (the dry run without `--run` is free).
- Do not widen the network surface: everything listens on loopback; keep the panel's `-agent-addr` loopback-only.
- Do not weaken a guard rail (`--owner-approved`, the changelog check in `feature ship`, the `notify-owner` rate limit, the
  merge guard, the done gate) to make a test pass. Fix the test or ask.
- Do not merge or push to `stage`/`main`, and do not run `feature ship`, unless the person who asked you said so in words. If you
  are a member of a thread's team, your lead's rules in `LEAD.md` and your skill in `claude/skills/jarvis/` apply.
- Do not add dependencies to `bin/` tools or the plugin; `vm_mcp.py` and `attachments/` have theirs, keep them few.
- Do not rename tools, files or statuses casually: the panel, the prompts, the contract tests and the docs all know them.

## Where to look when something is wrong

| Symptom | Look at |
|---|---|
| JARVIS silent on Telegram | `hermes status`; `pgrep -fa "gateway run --external-supervisor"`; `run/agent.log`; the dashboard's Logs page |
| A `vm` tool says "control panel not reachable" | is the panel running? `~/.config/control-panel/agent-token` present? `PANEL_AGENT_URL` |
| A tool is refused with `panel said 409/400: ...` | the message is the panel's reason (stale revision, wrong thread, done gate); read it, do not retry blindly |
| Buttons show as raw JSON on Telegram | is the plugin enabled (`hermes config get plugins.enabled`) and was the gateway restarted? |
| No fleet pings | `hermes cron list` shows `fleet-pings`? `~/.local/state/vm-agent/pings.json`; is the thread/pane in a thread's team (skipped on purpose)? |
| A lead did the wrong thing | its playbook is `hermes/LEAD.md` plus the `[Feature record]` block the panel adds; read both before blaming the model |
