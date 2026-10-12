# Data and privacy

## What tarcin keeps, and where

| Path | What |
|---|---|
| `~/.local/share/tarcin/profiles/default/session.db` | The device's keys. Anyone with this file can read your new messages. |
| `~/.local/share/tarcin/profiles/default/tarcin.db` | Your chats, messages and contacts |
| `~/.cache/tarcin/profiles/default/media/` | Downloaded media; always safe to delete |
| `~/.local/state/tarcin/tarcin.log` | The log, without message content, names or phone numbers |
| `~/.config/tarcin/config.toml` | Your configuration, if you wrote one |

The XDG variables move them (`$XDG_DATA_HOME`, `$XDG_CACHE_HOME`,
`$XDG_STATE_HOME`, `$XDG_CONFIG_HOME`). Directories are private to you
(0700) and files too (0600). tarcin does not encrypt them itself: use
full-disk encryption, such as LUKS.

tarcin sends nothing anywhere but to WhatsApp, has no telemetry and
opens no listening sockets.

## Backups

```sh
tarcin backup ~/tarcin-backup.tar.gz
```

writes your chats, messages, contacts and configuration to a new
archive, also while tarcin runs. Media are left out; they can be
downloaded again. The device's keys are left out too, because anyone
with them can read your new messages: after a restore, you link the
device again. `--with-keys` adds them; keep such a backup where only you
can read it. The archive's `README` says how to restore it.

Never copy `session.db` to a cloud drive or a shared disk.

## Logging out

```sh
tarcin logout
```

unlinks this device from your account, so that your phone no longer
lists it, and deletes its keys. tarcin then asks whether to delete your
messages and media too; the default is to keep them. If WhatsApp cannot
be reached, tarcin asks before it deletes the keys, because your phone
would go on listing the device until you remove it there.

## Deleting everything

After `tarcin logout`, delete the directories:

```sh
rm -rf ~/.local/share/tarcin ~/.cache/tarcin ~/.local/state/tarcin ~/.config/tarcin
```

To remove one chat from this computer only, open it and run
`:delete-local`; your phone keeps it.

## Messages that disappear

In a chat with disappearing messages, tarcin sends with the chat's timer
and deletes expired messages and their media files, at start and every
five minutes, as your phone does. View-once media show only a
placeholder.
