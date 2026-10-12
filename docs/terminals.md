# Terminals

tarcin works in any terminal with a UTF-8 locale, 16 colours and at
least 20×5 cells; 80 columns or more show the chat list and the chat
side by side. A few features depend on what the terminal supports.

| Feature | Needs | Without it |
|---|---|---|
| Copying a message (`y`), the passkey snippet (`ctrl+y`) | OSC 52 clipboard writes | Nothing is copied. Some terminals refuse OSC 52 or ask first. In tmux, `set -g set-clipboard on`. |
| Notifications only while you look away | Focus reporting (DECSET 1004) | tarcin cannot tell whether you are looking, and notifies only for chats other than the open one. In tmux, `set -g focus-events on`. |
| `shift+enter` for a new line | The kitty keyboard protocol | Use `alt+enter` or `ctrl+j`, which work everywhere. |
| Exact emoji widths | Unicode core mode 2027 (grapheme clusters), as in Ghostty, foot, WezTerm and Contour | See below. |

## Emoji and wide characters

Terminals measure some characters differently: an emoji with a
presentation selector ("❤️"), a family joined with zero-width joiners,
a skin tone or a flag can take one to six cells depending on the
terminal. When the terminal draws a line wider than the program
measured it, the line wraps and the screen shifts.

When the terminal reports Unicode core mode 2027, tarcin measures text
as grapheme clusters, as the terminal does, and shows everything as it
is. Otherwise it measures with wcwidth and, on screen only, simplifies
what terminals disagree on: it drops presentation selectors, joiners,
skin tones and tag characters, and spells flags as letters ("[TR]").
The messages themselves are unchanged; `y` and `v` show the text as
the list shows it.

## The QR code

The link screen draws the QR code with half blocks, black on white
whatever your colours, since many phones do not read an inverted code.
The code itself takes 57×29 cells. With its explanation the screen needs
about 61×40; a smaller terminal shows only the code and one line, which
needs 57×31. Anything smaller says how large the terminal must be: make
the window larger or the font smaller (`ctrl+minus` in most terminals).
Pressing `p` links with a pairing code instead, which needs no space.

## Hostile text

Text from WhatsApp never reaches your terminal raw: escape sequences
and control characters are removed, and bidi controls that could
reorder a line show as visible markers such as `[RLO]`. A message
cannot change your terminal's title, write to your clipboard or hide
text.

## Over SSH

tarcin works over SSH like any terminal program. Desktop notifications
need a D-Bus session, so over SSH the terminal bell rings instead;
`xdg-open` opens media on the remote machine, so set `media.open` to
something that suits you, or save media with `s`.
