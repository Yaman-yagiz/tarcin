# Configuration

tarcin reads `$XDG_CONFIG_HOME/tarcin/config.toml`, usually
`~/.config/tarcin/config.toml`. Every key is optional and the file
itself too: without it, every setting is the default. tarcin never
writes the file, and it holds no secrets, so it is safe in a dotfiles
repository.

```sh
tarcin config print-defaults > ~/.config/tarcin/config.toml   # a complete, commented start
tarcin config check                                           # validate it
tarcin config path                                            # where tarcin looks
```

An unknown key, a wrong type or a bad value is an error that names the
file, the line and the key, so a typo never goes unnoticed.
`--config FILE` reads another file.

`man 5 tarcin` lists every key with its default. The common changes:

## Privacy

```toml
[privacy]
read_receipts = false   # no blue ticks for the people who write to you
typing = false          # do not show "typing…" to others
presence = false        # never appear online; you then do not see others online either
```

Even with `presence` on, tarcin is online only while you use it: from a
key press until two minutes without one, or until the terminal loses
focus.

## Look

```toml
[ui]
theme = "high-contrast"   # "auto" (default), "default", "plain" or "high-contrast"
```

`default` uses your terminal's 16 colours, so it follows its theme.
`plain` uses no colours, only bold, underline and reverse.
`high-contrast` uses no colours and no faint text either, and reverses
selections and warnings, so everything shows at your terminal's full
contrast. `auto` is `plain` when the `NO_COLOR` environment variable is
set.

## Media

```toml
[media]
max_download_mib = 100       # refuse larger downloads
cache_mib = 2048             # the media cache keeps at most this much
save_dir = "~/Pictures/whatsapp"
open = ["xdg-open"]

[media.openers]
"image/*" = ["imv"]
"video/*" = ["mpv", "--keep-open"]
"application/pdf" = ["zathura"]
```

The file's path is added after the program's arguments, and programs
run without a shell. The cache (`~/.cache/tarcin`) is always safe to
delete.

## Notifications

```toml
[notifications]
enabled = true    # a notification for new messages while the terminal is not focused
preview = false   # say who wrote, not what
```

tarcin notifies only for live messages in chats that are not muted,
while the terminal does not have focus, one notification per chat.
Without a notification daemon it rings the terminal bell.

## Keys

See [keys](keys.md).

## More than one account

`--profile NAME` keeps an account apart, with its own data:
`tarcin --profile work`. The configuration is shared.
