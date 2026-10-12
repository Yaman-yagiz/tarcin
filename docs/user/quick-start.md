# Quick start

## Link your account

Run `tarcin`. The first time, it explains what it stores and the risk
of using an unofficial client; press Enter to go on. Then link this
device as you would link WhatsApp Web:

- **QR code:** on your phone, open WhatsApp → Settings → Linked devices
  → Link a device, and scan the code. It changes every 20 seconds. If
  the terminal is too small for it, tarcin says how large it must be.
- **Pairing code:** press `p`, type your phone number with its country
  code, and type the code tarcin shows into the phone. While linking,
  the phone lists the device as "Chrome (Linux)".
- **Passkey:** some accounts ask for a passkey. tarcin then shows a
  short snippet: open web.whatsapp.com in a browser, paste it into the
  developer console (`ctrl+y` copies it), and paste the line it prints
  back into tarcin.

Your chats and their recent history arrive in the background. The
phone sends older messages as you scroll back.

## Find your way

The screen has three panes: the chat list, the open chat and the
composer. `tab` moves between them, `?` shows every key and command, and
`ctrl+q` quits. Keys are vim-like:

| Key | Does |
|---|---|
| `j` / `k` | down / up |
| `gg` / `G` | top / bottom |
| `enter` | open the selected chat |
| `/` or `ctrl+k` | find a chat by name |
| `]u` / `[u` | next / previous chat with unread messages |
| `i` | write in the open chat |

With fewer than 80 columns, one pane shows at a time.

## Write

In the composer, `enter` sends and `shift+enter`, `alt+enter` or `ctrl+j`
starts a new line. `ctrl+x ctrl+e` opens the message in `$EDITOR`. `esc`
goes back to the messages.

Type `@` and the start of a name to mention someone in a group; `tab`
completes it.

## Act on a message

Select a message with `j` and `k`, then:

| Key | Does |
|---|---|
| `R` | reply |
| `r` | react: `1` to `6` pick 👍 ❤️ 😂 😮 😢 🙏, `0` removes yours |
| `e` | edit your message, within 20 minutes |
| `dd` | delete for everyone, after asking |
| `f` | forward: pick a chat by name, `enter` sends |
| `y` | copy the text |
| `v` | read the text in `$PAGER` |
| `o` / `s` | open / save the media |
| `gq` | jump to the quoted message |
| `ctrl+r` | send a failed message again |

## Media

Select the message with `j` and `k` in the chat (the status line then
shows `o open · s save`). `o` downloads and opens a photo, video, voice
message or document with `xdg-open`, or with a program you choose per
type (see [configuration](configuration.md)). `s` saves a copy to your downloads
folder; it never replaces a file. A file of a type tarcin does not
know is not opened, since your system would guess its type from its
content; save it with `s` instead, or set an opener for its type.

To send a file, type a caption if you like, then `:attach ~/Pictures/`
and press `tab` to complete the name.

## Search

`:find words` searches the messages of every chat; `:findin words`
searches the open chat. Matching ignores case and accents and treats
every Turkish i alike, so `kadin` finds `KADIN` and `Kadın`. `enter`
opens a result's chat at the message.

## Chats

`:new` starts a chat with someone you have not written to yet: a
contact by name (`:new ay` and `tab` complete it), or a phone number
with its country code (`:new +90 555 123 45 67`), which tarcin checks
with WhatsApp first. A chat that exists already just opens.

`:mute 8h` (or `1w`, `always`), `:pin` and `:archive` change the open
chat on all your devices; `:unmute`, `:unpin` and `:unarchive` undo
them, and `:archived` shows archived chats. `:members` shows a group's
members. `:delete-local` deletes a chat from this computer only; your
phone keeps it.

## Next

- [Configuration](configuration.md): privacy settings, programs,
  notifications and keys.
- [Data and privacy](data.md): backups and removing your data.
