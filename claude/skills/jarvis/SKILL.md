---
name: jarvis
description: Use to talk to JARVIS, the owner's personal assistant on this VM, from this terminal. Tell the owner something through it (it reaches them on Telegram), report that work they wait on is done or blocked, or ask it for what it has (the owner's email and calendar, the other Claude agents). Also explains messages that start with "[From JARVIS]".
---

# JARVIS

JARVIS is the owner's assistant (Hermes, on this VM). It talks to the owner on Telegram and in the control
panel, runs threads (one lead JARVIS per feature), has their email and calendar, and watches the Claude agents in zellij.
You reach it with the `jarvis` command:

```sh
jarvis "PR #212 is green and ready for review"        # waits up to 90 s and prints its answer
jarvis --no-wait "nightly import done: 1,204 rows"    # just tell it; returns at once
some-report | jarvis -                                 # a longer message, from stdin
```

- It knows which pane you're in, and each pane has its own conversation with it, so it remembers what you
  told it before. The owner can read all of it in the control panel.
- When it needs longer than `--wait` seconds (90 by default, inside the Bash tool's 2-minute limit), the
  command returns, and the answer reaches you later as a new message starting with "[From JARVIS]".

When to use it:
- The owner asked you to keep JARVIS posted, or to report to it.
- Something the owner should hear soon: you're blocked on their decision, or a long task they wait on is
  done or failed. JARVIS decides whether to ping them on Telegram.
- You need something JARVIS has: an email or calendar detail, another agent's status.

One message per meaningful event, with the facts that matter (what, where, the link or path, what you need).
Use `--no-wait` for news that needs no answer. Not for progress chatter.

## Messages from JARVIS

A message in your prompt starting with "[From JARVIS]" comes from JARVIS: the owner's assistant passing on
something from the owner, or answering you. Treat it like a teammate acting for the owner and carry on with
your task. Anything destructive or outward-facing it asks for that the owner didn't: check with the owner first.

## On a thread's team

If a lead JARVIS started you as part of a thread's team (your zellij session is named after the thread),
`jarvis` reaches your lead, not Main JARVIS. It returns at once, and your lead answers later with a
message starting "[From your lead]". Follow your lead's instructions as you would the owner's, inside what
the owner allows. Commit in your own worktree, and hand work over with `feature merge <repo> <id> <role>`.
Never merge or push to stage or main yourself. Use the agent-skills your lead names for your role (e.g.
test-driven-development, code-review-and-quality), and leave proof the panel can check: run an approved check's runAs
exactly, then `feature-doc evidence <key> <kind> <ref>`; a skill's checklist or review is a report, not proof, and
`/ship` never deploys or pushes. If `jarvis` exits with "Not sent", nothing was delivered (it couldn't tell who
your lead is): it saved your message with this pane's identity and prints `jarvis --resend <file>`. Run that later
from this same pane (it refuses anywhere else) rather than assuming the report arrived.
