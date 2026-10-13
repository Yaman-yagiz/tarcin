# Terminal UI architecture

- **Framework:** Bubble Tea v2, Lip Gloss v2 and the Bubbles v2
  `textarea` ([ADR-0004](../adr/0004-bubble-tea-tui.md)).

## 1. Rules

1. **One owner of UI state.** Only `Update` changes UI state.
   Background goroutines never touch models; they send messages through
   [`ui.Bridge`](event-pipeline.md#33-uibridge-coalescing).
2. **No I/O in `Update` or `View`.** Database reads, network operations,
   media decoding and process spawning all run in `tea.Cmd`s.
3. **`View` is O(visible rows).** It assembles pre-rendered, cached lines
   and never wraps or styles the whole history.
4. **Render only on change.** Events arrive in batches from the bridge,
   the frame rate is capped at 60 fps, and nothing is drawn while the
   app is idle. Bubble Tea flushes frames on a ticker, so the frame
   interval (16.7 ms) bounds the delay between a key press and the
   screen. Rendering itself takes well under a millisecond.
5. **All untrusted text is sanitised before display** (§7).

## 2. Layout

```text
┌ Chats ─────────────┐┌ Group: Family (128) ─────────── 🔇 ┐
│ ● Family        2m ││ Ayşe  09:41                         │
│   Work         12m ││   Akşam yemeği 8'de mi?             │
│   Ali           1h ││ Mehmet  09:42  ↩ Ayşe               │
│ ...                ││   Evet 👍                           │
│                    ││ ...                                 │
│                    │├─────────────────────────────────────┤
│                    ││ > message…                          │
└────────────────────┘└─────────────────────────────────────┘
 NORMAL │ online │ Family │ ? help
```

- Panes: the chat list, the message view, the composer, a status line
  and a `:` command line.
- Overlays: help, search, pairing (QR code or pairing code), the
  message-action menu, and confirmations.
- Narrow terminals (under 80 columns) show one pane at a time: the chat
  list, then the chat after it is opened.

## 3. Message view: virtualised list

Clients such as [whatscli](https://github.com/normen/whatscli) place
the whole history into a single text widget and re-parse it on every
change, which freezes them in busy groups. tarcin uses its own list
component (`internal/ui/msglist`) instead.

### 3.1 Window and anchor

- The model holds a **window** of at most about 600 messages around the
  viewport. These are loaded from SQLite with keyset queries (see
  [data model](data-model.md) §4).
- The scroll position is an **anchor**: (message key, line offset within
  that message). It is not an absolute line number. Loading more history
  above the window, or a new message arriving below it, therefore never
  makes the viewport jump.
- When the viewport comes within one screen of the window's edge, the
  list issues a `tea.Cmd` to load the next page (100 messages) and
  evicts the same number of messages from the far end. The page size
  was tuned on the UI benchmarks.
- If the user moves up while the oldest local message is in view, the
  list asks for older messages once per oldest message, and the app
  enqueues an on-demand history request job, unless the chat is marked
  as end-of-history; then the status line says "This is the start of
  the chat". Opening a short chat asks for nothing. Older messages that
  arrive go before the window, or are left for paging when the window
  does not reach the first message; they never count as new.
- Following new messages is automatic only while the view is at the
  bottom. Otherwise a "↓ N new" indicator appears.
- A chat with disappearing messages says so in its header ("messages
  disappear after 7 days"), and its messages carry ⏱. They are deleted
  when their time is up, with their cached media, as on the phone. A
  view-once photo, video or voice message shows a placeholder: WhatsApp
  opens it only on the phone.
- `]u` and `[u` open the next and the previous chat with unread
  messages, in list order, wrapping around.
- `:mute [8h|1w|always]` (8 hours by default), `:unmute`, `:pin`,
  `:unpin`, `:archive` and `:unarchive` change the chat selected in the
  list, or the open one: through WhatsApp's app state, so the phone and
  other devices follow, and in the store at once. At most three chats are
  pinned; archiving unpins.
- `:delete-local` deletes the selected or open chat, its messages and its
  media from this computer, after a y/n question; the phone keeps them.
- `:debug` shows the counters on the whole screen, refreshed every
  second: the store's queues, transactions and longest queue wait, the
  applier's skipped and refused events, the job pools, the bridge's
  batches, the UI's frames and render-cache hit rates, and the process's
  goroutines and heap. Esc closes it.
- `:find WORDS` searches the messages of all chats, `:findin WORDS` those
  of the open chat, through the store's full-text index: every word must
  start a word of the message, on textfold keys ("kahve" finds
  "Kahveler"). The 200 newest results show on the whole screen with
  chat, sender, time and the text around the first word; Enter opens
  the chat at the message, loading the window around it.
- A reply shows a line with the quoted message's sender and text: the
  copy in the window when it is loaded, else the preview the store reads
  with the reply, so a quote of an old message shows too. `gq` selects
  the quoted message, loading the window around it when it is outside.
- `y` copies the selected message's text to the clipboard with
  `wl-copy` (Wayland), `xclip` or `xsel` (X11), run without a shell
  with the text on standard input; without them, through the terminal
  (OSC 52), which works over SSH but which VTE terminals (GNOME Console,
  GNOME Terminal) ignore. `v` shows it in
  `$PAGER`, or `less` without one. Both use the text as the list shows
  it, sanitized; the pager reads it from its standard input, so it never
  goes to a file.
- `f` forwards the selected message: the chat search opens with the
  prompt "forward to /", Enter forwards to the selected match, and Esc
  cancels. The open chat stays open; the copy shows in the target chat,
  marked forwarded. Text, media, locations and contacts can be
  forwarded, but not deleted or view-once messages, nor own messages
  that are not sent.
- Reactions show in a line below the message, by emoji and count, most
  frequent first; this account's own is highlighted. `r` opens a picker
  in the status line: 1 to 6 react with 👍 ❤️ 😂 😮 😢 🙏, 0 removes
  this account's reaction, Esc cancels; `:react EMOJI` reacts with any
  emoji. A sent reaction is stored at once with the local millisecond
  clock, as the reaction itself carries, and always after this account's
  previous change, so changing it quickly works.
- The chat header shows "typing…" while someone types in the open chat,
  and for a direct chat whether the peer is online or when they were
  last seen ("today 14:02", "yesterday 21:04"). A typing indicator whose
  end is lost disappears after 25 seconds. Typing in the composer shows
  this account typing to the chat, renewed every 10 seconds and ended
  after 5 seconds without a change or when the message is sent, as
  `privacy.typing` allows.
- A mention, "@" and the user part of a JID in the text, shows as "@"
  and the person's current name, highlighted, "@You" most; a message
  that mentions this account is marked "@ mentions you" in its header.
  In a group, typing "@" and the start of a name offers up to five
  members in a line above the input: Tab takes the highlighted one,
  ctrl+n and ctrl+p move, Esc closes them. The name stays readable in
  the composer and is sent as WhatsApp sends mentions, with the JID in
  the message's context.
- `e` puts this account's own text message, if it is at most 20 minutes
  old, in the composer for editing, under a line that says so; Enter
  sends the edit, the first Esc cancels it, and the draft put aside comes
  back either way. `dd` asks in the status line whether to delete the
  message for everyone (y/n): this account's own, for two days after
  sending, or as a group admin anyone's.
- `R` makes the composer answer the selected message: a line above the
  input names it, and the first Esc drops the reply before a second
  leaves the composer. Each chat keeps its own reply, like its draft.

### 3.2 Render cache

- Cache key: (message key, revision, width, theme ID, width method).
- Cached value: the rendered lines (styled strings) and the height.
- The revision is bumped on edits, reactions, receipts and media-state
  changes.
- The cache is a bounded LRU (default 4,096 entries).
- Wrapping a *new* message is a pure function of its text and the width.
  Batches of new messages are wrapped inside a `tea.Cmd`, and the result
  comes back ready to insert.
- On resize, the anchor stays the same. Old cache entries simply miss
  because the width is part of the key, so only the visible messages are
  re-wrapped. Renders are debounced by about 50 ms while the user is
  dragging the window.

### 3.3 `View`

1. Start at the anchor.
2. Append cached lines to a `strings.Builder` until the pane is full.
3. Add the cached header, composer and status line.

Lip Gloss styles are created once per theme, never once per frame.
Benchmarks assert that allocations in `View` and in scrolling do not grow
with the size of the history ([performance](performance.md) B5).

## 4. Chat list

- The chat list is virtualised the same way: only the visible rows are
  rendered.
- Rows come from the `chats` table, which holds **conversations, not
  contacts**. It is sorted by pinned, then the latest
  activity, then the sequence number.
- **Moving the cursor does not open a chat.** A chat opens on `Enter`.
  An optional preview loads after a 150 ms debounce and reads only the
  last page.
- A row update for an incoming message changes only that row and its
  position in the list. The list is never rebuilt.

## 5. Input model

### 5.1 Modes

| Mode | Purpose |
|---|---|
| Normal | Navigate panes and lists, select messages, run actions |
| Insert | Compose in the textarea |
| Command | `:`-commands such as `:open`, `:search`, `:mute`, `:logout`, `:quit` |

Keys are bound to **named actions per context**, in the style of aerc and
iamb (`internal/ui/keymap`). Bindings are overridden in `config.toml`
under `[keys.global]`, `[keys.navigation]`, `[keys.chatlist]`,
`[keys.messages]` and `[keys.compose]`. An override replaces all
bindings of an action, and an empty list unbinds it.
`tarcin --dump-keys` shows the name of every key the terminal sends,
which is the name to use in the config.

- Lookups walk a chain: the chat list and the message view fall back to
  `navigation` and then `global`, and the composer falls back to
  `global`.
- No binding may be a prefix of another in the same chain. That makes
  sequences such as `gg` resolve at once, without vim-style timeouts;
  conflicting configs are rejected with an error that names the keys.
- In the composer, text keys (printable characters and space) always go
  to the text: global bindings such as `?` only apply outside it, and
  compose bindings must not start with a text key.
- Key names are normalized to the form Bubble Tea reports, so
  `Ctrl+Q`, `alt+ctrl+x` and `shift+g` work as written.
- The composer is a Bubbles `textarea` with readline-style editing keys
  (`ctrl+a`/`ctrl+e`, `ctrl+w`, `ctrl+u`, word movement with `alt`). Enter
  belongs to the keymap (send), and global non-text bindings still apply
  while composing. That means `ctrl+k` opens the search there instead of
  deleting to the end of the line.

### 5.2 Default bindings

| Context | Keys |
|---|---|
| Global | `Ctrl-q` quit · `?` help · `:` command line · `Ctrl-k` or `/` search chats · `Tab` / `Shift-Tab` switch pane · `]u` / `[u` next / previous unread |
| Lists | `j` / `k`, arrow keys · `gg` / `G`, Home / End · `Ctrl-d` / `Ctrl-u` · `Ctrl-f` / `Ctrl-b`, PgDn / PgUp · `Enter` or `l` open |
| Selected message | `i` / `a` compose · `R` reply · `r` react · `f` forward · `e` edit own · `dd` delete (with confirmation) · `y` copy · `o` open media · `s` save media · `gq` jump to quoted · `gx` open a link · `v` view in `$PAGER` · `Ctrl-r` send a failed message again |
| Compose | `Enter` send · `Shift-Enter` / `Alt-Enter` / `Ctrl-j` newline · `Ctrl-x Ctrl-e` edit in `$EDITOR` · `Esc` back to normal · readline keys (`Ctrl-a`, `Ctrl-e`, `Ctrl-w`, `Ctrl-u`) |

We avoid Alt chords as primary bindings, because some keyboard layouts
type accented characters with Alt (see nchat #535). Where an Alt chord is
offered, an alternative binding exists. `Shift-Enter` needs the kitty
keyboard protocol; the other newline bindings work on legacy terminals.

## 6. External programs

- `$VISUAL` / `$EDITOR` run through `tea.ExecProcess`. The draft is
  written to a temp file with mode 0600 in a private directory and
  deleted afterwards.
- Opening media (`o`) uses `xdg-open`, or a per-MIME opener from
  `[media.openers]` in the config (`"image/*" = ["imv"]`). Openers are
  run with `exec.Command(name, args...)`, **never through a shell**, with
  the cache file's path as the last argument, detached from the
  terminal. `s` saves a copy to `media.save_dir` (default
  `$XDG_DOWNLOAD_DIR` or `~/Downloads`), never over an existing file.
  Both download the media first if needed; the placeholder says
  "downloading…", "download failed" or "no longer available".
- `:attach PATH` sends a file to the open chat, with the composer's text
  as its caption (and its reply and mentions); Tab on the command line
  completes the path. JPEG, PNG and WebP go as images, MP4 as video,
  Ogg, MP3, M4A and AAC as audio, by their content; anything else as a
  document named after the file. The file must be at most
  `media.max_upload_mib`. A copy goes into the media cache, so the sent
  message opens at once and a failed one is sent again with `ctrl+r`
  even after the original is gone.
- Long messages open in `$PAGER` the same way.
- Notifications go over D-Bus (see [packaging](packaging.md) §4,
  `internal/notify`). They are skipped when the terminal has focus
  (focus reporting, DECSET 1004), unless `notifications.when` is
  `"other-chats"`; the open chat notifies only while the terminal is
  known to lack focus; for muted chats; for history (only live, unread messages from
  others notify) and for a backlog (messages more than two minutes old,
  delivered after tarcin was offline). One notification per chat, the
  newest replacing the last. `[notifications] preview = false` leaves the
  text out. Without a notification service, the terminal bell rings.
  While the terminal lacks focus, the open chat's new messages also stay
  unread (see [event pipeline](event-pipeline.md)).

## 7. Terminal-safe rendering

All text that comes from the network is sanitised by
`internal/sanitize` before it reaches a styled string: message bodies,
display names, group subjects, file names and link previews.

- C0 and C1 control characters are stripped, except `\n` and `\t`.
  `\t` is expanded to spaces.
- `ESC` and every escape sequence (CSI, OSC, DCS, APC, PM, SOS) are
  stripped. This blocks title or clipboard injection through OSC 52,
  hyperlink spoofing through OSC 8, and changes to terminal modes.
- **BiDi controls** (U+202A–U+202E, U+2066–U+2069) are removed from
  names and shown visibly in bodies. This defeats "Trojan Source"-style
  spoofing.
- Zero-width and invisible characters are collapsed in display names and
  kept in bodies.
- Very long messages are cut off with a "show more" action that opens
  the pager. The limit can be configured.
- Web addresses in message bodies are OSC 8 hyperlinks, part by part
  where the wrap breaks them, so a click opens the whole address; a
  terminal that guesses links from the screen would see only the part
  before the break. The target is always the address the text shows,
  after sanitizing: http and https only, an ASCII host, no user name
  (`https://bank.example@evil.example` is not linked), at most 2 KiB,
  the rest of the path percent-escaped. `gx` opens the selected
  message's links in turn with the media opener. Terminals without
  OSC 8 ignore the sequences.

The sanitiser has fuzz tests (`go test -fuzz`) asserting that its output
never contains `ESC`, C1 bytes or BiDi controls.

## 8. Unicode, width and locale

- Layout measures text with the same width method as Bubble Tea's
  renderer: wcwidth by default, grapheme clusters once the terminal
  reports Unicode core mode 2027. The root model mirrors the renderer's
  switch.
- In wcwidth mode, emoji sequences are where terminals disagree: one
  terminal draws "❤️" or a ZWJ family as 2 cells while naive wcwidth
  counts 1 or 6, and a flag can take 2, 3 or 4 cells. When a terminal
  finds a line wider than the renderer does, the line wraps and the whole
  frame shifts. `uikit.Display` therefore normalizes what is *drawn*:
  - it removes variation selectors, zero-width joiners, skin-tone
    modifiers and tag characters
  - it spells flags as letters (`[TR]`)

  A test renders demo frames and asserts that every line measures the
  same under both width models. Message content itself is never changed.
- Known limitations for each terminal are documented in
  [terminals](../terminals.md).
- Display names are truncated by cluster, never by byte or rune, so that
  emoji are never cut in half.
- Search uses `internal/textfold`, a locale-independent search key:
  Unicode full case folding, diacritics and invisible characters
  removed, and every Turkish i form (I, ı, İ, i) mapped to `i`. So
  "kadin" finds "Kadın" and "KADIN" whatever the user's locale or
  keyboard. The same key is stored in the FTS5 index, so in-memory and
  database search agree.

## 9. Theming and accessibility

- The default theme uses the terminal's 16-colour ANSI palette, so it
  follows the user's terminal theme. 256-colour and truecolor themes are
  optional.
- `ui.theme` in `config.toml` picks `default`, `plain` (no colour, only
  bold, underline and reverse), `high-contrast` or `auto`, the default:
  `plain` when [`NO_COLOR`](https://no-color.org) is set, else `default`.
- `high-contrast` uses no colour and no faint text, whose contrast
  depends on the palette; it marks with bold, underline and reverse
  video, and reverses selections and warnings, so the terminal's own
  foreground and background keep full contrast on light and dark themes.
- Only the theme colours the screen: the composer's and the command
  line's widgets have their own colours, and the terminal cursor's,
  removed. A test draws frames with `plain` and `high-contrast` and fails
  on any colour or faint attribute.
- Hints that name a key, such as the quit key on the link screen, show
  the binding in effect.
- Information is never conveyed by colour alone. Read ticks, mentions and
  unread markers also differ by symbol or text.

## 10. Testing

- `Update` and `View` are tested as plain functions: given a model and a
  message, assert the new model, the commands it returns and the rendered
  output.
- Golden-file snapshots of key screens use a small helper
  (`internal/ui/golden_test.go`, `make golden`), at 80×24 and 120×40.
- The message list has benchmarks of `View` and scrolling with 10k and
  100k synthetic messages, including allocation assertions.

## 11. Linking a device

A device that is not linked to an account yet starts on the link screen
(`internal/ui/linkscreen`). It takes the whole screen and every key but
quit. It opens on the **first-run notice**: tarcin is unofficial and the
account may be restricted or banned for using it; messages and device
keys are stored unencrypted in the named directory, so full-disk
encryption matters; tarcin is not affiliated with WhatsApp or Meta
([security](security.md) §7,
[ADR-0010](../adr/0010-encryption-at-rest-deferred.md)). Nothing
connects until the user presses Enter, and the status line says "not
linked" meanwhile. The screen then offers the three ways WhatsApp links
a companion device:

| Way | What the user does | Notes |
|---|---|---|
| QR code (default) | Scans the code with the phone (Settings → Linked devices → Link a device) | Drawn with half blocks, black on white whatever the terminal's colours, because many scanners do not read an inverted code, with a two-module margin, which phone cameras read on a screen. It changes every 20 seconds. A terminal too small for the whole screen shows only the code and one line (57×31 for a real code); a smaller one says how large it must be and suggests a smaller font or the phone number. |
| Pairing code (`p`) | Enters the phone number, then types the code tarcin shows on the phone | WhatsApp only accepts a common browser name for this, so the phone shows "Chrome (Linux)" while linking |
| Passkey hand-off | Some accounts require a passkey. The user opens web.whatsapp.com in a browser, runs a three-line snippet in the console (ctrl+y copies it), and pastes the line it prints | A passkey assertion is bound to the web.whatsapp.com origin, so no terminal program can make it; the snippet hands the challenge to the user's own browser and authenticator. If the phone asks, the user then compares a code (y/n). |

Linking runs inside the backend's `Run`, with the event handler already
registered: the phone starts the history sync as soon as the link is
made, and an event dispatched with no handler waiting would be
acknowledged and lost. When the codes run out, the screen says so and
waits for Enter instead of showing new codes to someone who may have
walked away. `tarcin --demo --demo-link` shows the screen with the fake
backend. Should the device be unlinked while tarcin runs and linking
start again, the notice is not shown a second time.

## 12. Members view

`:members` shows the participants of the open group on the whole
screen (`internal/ui/members`): this account first, then the owners,
the admins and the members, each sorted by display name on its textfold
key (so "Çiğdem" sorts among the C's), and people without a name last.
The header shows the group's title, its member count, its description
and whether only admins may send or edit it.

- A group can have thousands of members, so the view reads them from
  the store in pages of 200, as the selection nears the end of what is
  loaded. `G` loads to the end. Only the rows on screen are rendered.
- The navigation keys move the selection; Esc, `q` or Enter close the
  view.
- When the group's members or metadata change, the view reloads and
  keeps the selection.
- Opening a group fetches its member list from the server in the
  background if the stored one is missing or older than a day
  ([event pipeline](event-pipeline.md) §3.5); the view updates when it
  arrives.
