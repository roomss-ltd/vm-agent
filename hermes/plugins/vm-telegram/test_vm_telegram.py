"""Self-check, with fakes for Telegram and the adapter. Run with Hermes' Python (python-telegram-bot):
   ~/.hermes/installs/*/environments/<current>/venv/bin/python hermes/plugins/vm-telegram/test_vm_telegram.py"""
import asyncio
import os
import sys
import tempfile
from pathlib import Path
from types import SimpleNamespace as NS

sys.path.insert(0, os.path.dirname(__file__))
import __init__ as p  # noqa: E402  (the plugin module)

p.STORE = Path(tempfile.mkdtemp()) / "tg-buttons.json"

# parsing: the block leaves the text; send and url buttons stay, panel-only ones go
reply = 'Build the cuts?\n\n```buttons\n[{"label": "Yes", "send": "Yes, build cuts 1-4"}, {"label": "No", "send": "No"}, ' \
        '{"label": "Open pane", "open": "pane:dev-1:27"}, {"label": "The PR", "url": "https://github.com/x/y/pull/1"}]\n```'
text, buttons = p.split_buttons(reply)
assert text == "Build the cuts?", text
assert [b["label"] for b in buttons] == ["Yes", "No", "The PR"], buttons
assert p.split_buttons("plain") == ("plain", [])
assert p.split_buttons("x\n```buttons\nnot json\n```") == ("x", [])
assert p.split_buttons('x\n```buttons\n[{"label": "A", "send": "a"}]')[1] == [{"label": "A", "send": "a"}]  # unclosed
assert [[i for i, _ in r] for r in p.rows([{"label": "a"}, {"label": "b"}, {"label": "a much longer label here"}, {"label": "c"}])] == [[0, 1], [2], [3]]


class Bot:
    """Stand-in for the Telegram bot: records the keyboards it is asked to attach."""

    def __init__(self):
        self.markups = []

    async def edit_message_reply_markup(self, chat_id, message_id, reply_markup):
        self.markups.append((chat_id, message_id, reply_markup))


class Adapter:
    """Stand-in for Hermes' Telegram adapter: send() records the text, handle_message() records the replayed tap."""

    def __init__(self):
        self._bot, self.sent, self.events = Bot(), [], []

    async def send(self, chat_id, content, reply_to=None, metadata=None):
        self.sent.append(content)
        return NS(success=True, message_id="42")

    def build_source(self, **kw):
        return kw

    async def handle_message(self, event):
        self.events.append(event)


class Native:
    """Stand-in for the python-telegram-bot application: keeps the handlers the plugin registers."""

    def __init__(self):
        self.handlers = []

    def add_handler(self, h):
        self.handlers.append(h)


async def main():
    """Send a reply with buttons, then tap them as a stranger, as the owner, and again (one answer per message)."""
    a, n = Adapter(), Native()
    p._factory(n, a)
    p._factory(n, a)  # a reconnect doesn't wrap twice
    await a.send("7", reply)
    assert a.sent == ["Build the cuts?"], a.sent
    chat, mid, kb = a._bot.markups[0]
    assert (chat, mid) == ("7", 42)
    first = kb.inline_keyboard[0][0]
    assert first.text == "Yes" and first.callback_data.startswith("vb:"), first
    assert kb.inline_keyboard[1][0].url == "https://github.com/x/y/pull/1"
    await a.send("7", "no buttons here")
    assert a.sent[-1] == "no buttons here" and len(a._bot.markups) == 1

    answers, edits = [], []

    async def answer(text=None):
        answers.append(text)

    async def edit(markup):
        edits.append(markup)

    def tap(user, data):
        q = NS(data=data, from_user=NS(id=user, full_name="Owner"), answer=answer, edit_message_reply_markup=edit,
               message=NS(chat=NS(id=7), message_id=42, text="Build the cuts?", caption=None))
        return n.handlers[-1].callback(NS(callback_query=q), None)

    os.environ["TELEGRAM_ALLOWED_USERS"] = "111"
    await tap(222, first.callback_data)
    assert answers[-1] == "Not allowed" and not a.events
    await tap(111, first.callback_data)
    e = a.events[-1]
    assert e.text == "Yes, build cuts 1-4" and e.source["user_id"] == "111" and e.reply_to_message_id == "42", e
    assert edits[-1].inline_keyboard[0][0].text == "✓ Yes"
    await tap(111, first.callback_data)  # one answer per message
    assert answers[-1].startswith("That choice has expired") and len(a.events) == 1
    assert len(n.handlers) == 2 and n.handlers[-1].pattern.pattern == "^vb:"


asyncio.run(main())
print("ok")
