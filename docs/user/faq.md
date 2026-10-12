# FAQ

## Is my account at risk?

Yes. tarcin is an unofficial client: it is not made or endorsed by
WhatsApp or Meta, and it speaks WhatsApp's protocol through the
reverse-engineered [whatsmeow](https://github.com/tulir/whatsmeow)
library. WhatsApp's terms do not allow unofficial clients, and an
account that uses one can be restricted or banned. tarcin sends nothing
the official clients would not, and it has no bulk messaging,
scheduling, bots or scraping, but that cannot rule the risk out. Do not
link an account you cannot afford to lose.

## Are my messages still end-to-end encrypted?

In transit, yes: tarcin is a linked device, like WhatsApp Web, and uses
the same encryption. On your computer, messages are decrypted and kept
in a local database that tarcin does not encrypt; it relies on file
permissions and on your disk encryption. See [data and
privacy](data.md).

## Does my phone need to stay online?

No. Linked devices work on their own. WhatsApp itself unlinks them when
the phone has not been used for about two weeks.

## Why does my phone list the device as "Chrome (Linux)"?

When you link with a pairing code, WhatsApp accepts only the name of a
common browser. Linked with a QR code, the device shows as tarcin.

## Can I use more than one account?

Yes, with profiles: `tarcin --profile work`. Each profile has its own
data and its own link.

## What does tarcin not do?

Calls, statuses and payments. Polls,
stickers, locations and contact cards show as placeholders for now;
images show as a line you open with `o`.

## tarcin says "update tarcin"

WhatsApp stopped accepting this version. Update it (see
[install](install.md#update)); releases follow such changes quickly.

## It does not connect

tarcin reconnects by itself when the network returns; `:reconnect`
tries at once. If it keeps failing, set `level = "debug"` in the
`[log]` section of the [configuration](configuration.md), run tarcin
again and look at `~/.local/state/tarcin/tarcin.log`. The log holds no
message content, names or phone numbers, but read it before you share
it.

## Emoji or names look misaligned

Terminals disagree about how wide some emoji are. tarcin draws emoji
sequences in a form every terminal measures alike unless the terminal
tells it how it measures them. See [terminals](../terminals.md).

## I get no notifications

Desktop notifications need a notification daemon (most desktops have
one; on a window manager, install `dunst` or `mako`) and a D-Bus session.
By default they appear only while the terminal does not have focus,
which needs focus reporting (in tmux: `set -g focus-events on`); set
`when = "other-chats"` under `[notifications]` to get them while you
use the terminal too, for the chats you are not reading. They never
appear for muted chats or old messages. Without a daemon, for example over SSH, tarcin
rings the terminal bell instead.

## Where do I report a bug?

In the project's issue tracker. Report security problems privately, as
[SECURITY.md](../../SECURITY.md) explains.
