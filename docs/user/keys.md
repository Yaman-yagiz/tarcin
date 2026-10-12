# Keys

`?` in tarcin shows every key and command as currently bound.

## Defaults

| Where | Keys |
|---|---|
| Everywhere | `ctrl+q` quit · `?` help · `:` command line · `/` or `ctrl+k` find a chat · `tab` / `shift+tab` next / previous pane · `]u` / `[u` next / previous unread chat |
| Lists | `j` / `k` or arrows · `gg` / `G` or `home` / `end` · `ctrl+d` / `ctrl+u` half a page · `ctrl+f` / `ctrl+b` or `pgdown` / `pgup` a page |
| Chat list | `enter` or `l` open |
| Messages | `i` / `a` write · `R` reply · `r` react · `e` edit · `dd` delete for everyone · `f` forward · `y` copy · `v` read in `$PAGER` · `o` open media · `s` save media · `gq` jump to the quoted message · `ctrl+r` send again |
| Composer | `enter` send · `shift+enter`, `alt+enter` or `ctrl+j` new line · `ctrl+x ctrl+e` edit in `$EDITOR` · `esc` back · readline keys such as `ctrl+a`, `ctrl+e`, `ctrl+w`, `ctrl+u` |

`shift+enter` needs a terminal with the kitty keyboard protocol; the
other new-line keys work everywhere.

## Commands

| Command | Does |
|---|---|
| `:open NAME` | open the first chat whose name matches |
| `:search TEXT` | filter the chat list |
| `:find WORDS`, `:findin WORDS` | search the messages of all chats, or of the open one |
| `:attach PATH` | send a file, with the composer's text as caption (`tab` completes the path) |
| `:react EMOJI` | react with any emoji |
| `:members` | the members of the open group |
| `:mute [8h\|1w\|always]`, `:unmute` | mute the chat |
| `:pin`, `:unpin` | pin the chat (at most three) |
| `:archive`, `:unarchive`, `:archived` | archive the chat, or show archived chats |
| `:delete-local` | delete the chat from this computer only |
| `:reconnect` | connect to WhatsApp again now |
| `:debug` | counters: queues, caches, frames |
| `:help`, `:quit` | help, quit |

## Changing keys

Bindings live in `[keys.CONTEXT]` tables of the
[configuration](configuration.md). The contexts are `global`,
`navigation`, `chatlist`, `messages` and `compose`; `man 5 tarcin`
lists every action. A list replaces all the bindings of its action, and
an empty list unbinds it:

```toml
[keys.messages]
reply = ["R", "ctrl+r"]
retry = []

[keys.global]
help = ["?", "f1"]
```

Keys in a sequence are separated by spaces (`"ctrl+x ctrl+e"`); a word
that is not a key name is its letters typed in order (`"gg"`). No
binding may be the start of another. `tarcin --dump-keys` shows the
name of each key you press.
