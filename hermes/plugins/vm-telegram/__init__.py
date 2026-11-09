"""Reply buttons on Telegram: the vm-telegram Hermes plugin of the vm-agent repo.

JARVIS ends a reply with the same ```buttons block the control panel reads:
    [{"label": "Yes, build it", "send": "Yes, build cuts 1-4"}, {"label": "Open the PR", "url": "https://…"}]
On Telegram the block leaves the text and becomes inline buttons under that message: "send" ones answer for
the owner (the tap is their reply, as if typed), "url" ones open the link. A tapped message keeps a
"✓ <label>" in place of its buttons, so the choice stays visible. Panel-only buttons ("open") are left out.

Hermes has no hook for outgoing messages, so this wraps the Telegram adapter's send() (its final replies,
cron and fleet deliveries all go through it) and adds a callback handler for its own "vb:" buttons.
"""
from __future__ import annotations

import json
import logging
import os
import re
import secrets
from contextlib import suppress
from pathlib import Path

logger = logging.getLogger(__name__)

BLOCK = re.compile(r"\n*```buttons[ \t]*\n(.*?)(?:```|\Z)", re.S)
STORE = Path(os.path.expanduser("~/.local/state/vm-agent/tg-buttons.json"))  # taps still work after a restart
KEEP = 300
_store: dict | None = None


def split_buttons(text: str):
    """(text without the buttons block, the buttons Telegram can show). A block that doesn't parse is
    dropped from the text too: raw JSON on the phone helps nobody."""
    m = None
    for m in BLOCK.finditer(text or ""):
        pass
    if not m:
        return text, []
    clean = (text[:m.start()] + text[m.end():]).strip()
    try:
        raw = json.loads(m.group(1))
    except ValueError:
        logger.warning("vm-telegram: a buttons block that isn't JSON was dropped")
        return clean, []
    out = []
    for b in raw if isinstance(raw, list) else []:
        if not isinstance(b, dict) or not str(b.get("label") or "").strip():
            continue
        label = str(b["label"]).strip()[:60]
        if str(b.get("send") or "").strip():
            out.append({"label": label, "send": str(b["send"]).strip()[:4000]})
        elif re.match(r"https?://", str(b.get("url") or "")):
            out.append({"label": label, "url": str(b["url"])})
    return clean, out[:8]


def rows(buttons: list) -> list:
    """Short labels two to a row, long ones alone."""
    out, pair = [], []
    for i, b in enumerate(buttons):
        if len(b["label"]) > 18:
            if pair:
                out.append(pair)
                pair = []
            out.append([(i, b)])
        else:
            pair.append((i, b))
            if len(pair) == 2:
                out.append(pair)
                pair = []
    if pair:
        out.append(pair)
    return out


def _load() -> dict:
    """The saved button sets by key (read from STORE once, then kept in memory)."""
    global _store
    if _store is None:
        try:
            _store = json.loads(STORE.read_text())
        except (OSError, ValueError):
            _store = {}
    return _store


def _save() -> None:
    """Write the button sets to STORE (the newest KEEP of them), via a temp file so a crash can't corrupt it."""
    s = _load()
    for k in list(s)[:-KEEP]:
        del s[k]
    with suppress(OSError):
        STORE.parent.mkdir(parents=True, exist_ok=True)
        tmp = STORE.with_suffix(".tmp")
        tmp.write_text(json.dumps(s))
        tmp.replace(STORE)


def remember(buttons: list) -> str:
    """Save a message's buttons under a new short key, which goes into their callback data; returns the key."""
    key = secrets.token_hex(4)
    _load()[key] = buttons
    _save()
    return key


def allowed(user_id) -> bool:
    """True when the Telegram user id is in TELEGRAM_ALLOWED_USERS (only the owner may tap a button)."""
    users = os.environ.get("TELEGRAM_ALLOWED_USERS", "")
    return str(user_id) in {u.strip() for u in users.split(",") if u.strip()}


def _factory(native, adapter):
    """Hermes calls this when the Telegram adapter connects: wrap its send() and register the button-tap handler."""
    from telegram import InlineKeyboardButton, InlineKeyboardMarkup
    from telegram.ext import CallbackQueryHandler
    from gateway.platforms.event import MessageEvent, MessageType

    def keyboard(key, buttons):
        """The inline keyboard for a message: url buttons open links, the others send "vb:<key>:<n>" taps."""
        return InlineKeyboardMarkup([[InlineKeyboardButton(b["label"], url=b["url"]) if "url" in b
                                      else InlineKeyboardButton(b["label"], callback_data=f"vb:{key}:{i}") for i, b in row]
                                     for row in rows(buttons)])

    if not getattr(adapter, "_vm_buttons", False):  # connect() runs again on a reconnect
        adapter._vm_buttons = True
        send = adapter.send

        async def send_with_buttons(chat_id, content, reply_to=None, metadata=None):
            """The adapter's send() with a ```buttons block turned into inline buttons on the sent message."""
            text, buttons = split_buttons(content)
            if text == content:
                return await send(chat_id, content, reply_to, metadata)
            res = await send(chat_id, text or "👇", reply_to, metadata)
            if buttons and getattr(res, "success", False) and getattr(res, "message_id", None):
                try:
                    await adapter._bot.edit_message_reply_markup(
                        chat_id=chat_id, message_id=int(res.message_id), reply_markup=keyboard(remember(buttons), buttons))
                except Exception as e:  # the message is out; only its buttons are missing
                    logger.warning("vm-telegram: couldn't add buttons: %s", e)
            return res

        adapter.send = send_with_buttons

    async def tapped(update, context):
        """A tap on one of our buttons: check the user, replay the button's text to JARVIS as the owner's reply."""
        q = update.callback_query
        _, key, idx = (q.data.split(":") + ["", ""])[:3]
        if key == "done":
            return await q.answer()
        if not allowed(q.from_user.id):
            return await q.answer("Not allowed")
        buttons = _load().get(key)
        if not buttons or not idx.isdigit() or int(idx) >= len(buttons) or "send" not in buttons[int(idx)]:
            return await q.answer("That choice has expired: reply in words")
        b = buttons[int(idx)]
        await q.answer()
        _load().pop(key, None)  # one answer per message
        _save()
        with suppress(Exception):
            await q.edit_message_reply_markup(InlineKeyboardMarkup([[InlineKeyboardButton("✓ " + b["label"], callback_data="vb:done")]]))
        msg = q.message
        source = adapter.build_source(chat_id=str(msg.chat.id), chat_type="dm", user_id=str(q.from_user.id),
                                      user_name=q.from_user.full_name)
        await adapter.handle_message(MessageEvent(
            text=b["send"], message_type=MessageType.TEXT, source=source,
            reply_to_message_id=str(msg.message_id), reply_to_text=(msg.text or msg.caption or "")[:500],
            reply_to_is_own_message=True))

    native.add_handler(CallbackQueryHandler(tapped, pattern=r"^vb:"))


def register(ctx):
    """Hermes plugin entry point."""
    ctx.register_telegram_handler(_factory)
