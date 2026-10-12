tarcin(5)

# NAME

tarcin - configuration file of the tarcin WhatsApp client

# SYNOPSIS

_$XDG_CONFIG_HOME/tarcin/config.toml_, usually
_~/.config/tarcin/config.toml_

# DESCRIPTION

The configuration is a TOML file. Every key is optional: a missing key
keeps its default, and without the file every setting is the default.
An unknown key, a wrong type or a bad value is an error that names the
file, the line and the key, so a typo never goes unnoticed. tarcin never
writes this file, and it holds no secrets.

*tarcin config print-defaults* prints a complete file with the defaults
and comments; *tarcin config check* validates the file.

# PRIVACY

*[privacy]*

*read_receipts* = _true_
	Tell senders when you have read their messages (blue ticks).

*typing* = _true_
	Show a chat when you are typing in it.

*presence* = _true_
	Show your contacts when you are online: only while you use tarcin,
	from a key press until two minutes without one or until the terminal
	loses focus. Off, you also do not see whether they are online.

# INTERFACE

*[ui]*

*theme* = _"auto"_
	*"default"* follows the 16 colors of your terminal's theme;
	*"plain"* uses none, only bold, underline and reverse;
	*"high-contrast"* uses no colors and no faint text, and reverses
	selections and warnings, for full contrast on any terminal theme.
	*"auto"* is *"default"*, or *"plain"* when the *NO_COLOR* environment
	variable is set.

# MEDIA

*[media]*

*max_download_mib* = _100_
	Downloads larger than this many MiB are refused, by the size the
	message declares and while streaming. From 1 to 4096.

*max_upload_mib* = _100_
	The largest file *:attach* sends, in MiB. From 1 to 2048.

*cache_mib* = _2048_
	The media cache (_$XDG_CACHE_HOME/tarcin_) keeps at most this many
	MiB; the least recently opened files go first. At least 16. The
	cache is always safe to delete.

*save_dir* = _""_
	Where *s* saves media: an absolute path, or one that starts with
	_~/_. Empty: _$XDG_DOWNLOAD_DIR_, or _~/Downloads_. A saved file never
	replaces another: "name (1).pdf" is used instead.

*open* = _["xdg-open"]_
	The program that opens media (*o*), with its arguments; the file's
	path is added at the end. It runs without a shell.

*[media.openers]*
	Programs for some MIME types instead of *open*, by type or by
	family:

```
[media.openers]
"image/*" = ["imv"]
"application/pdf" = ["zathura"]
```

# NOTIFICATIONS

*[notifications]*

*enabled* = _true_
	A desktop notification for each new message while the terminal does
	not have focus: not for muted chats, history or a backlog. Without a
	desktop session the terminal bell rings instead.

*preview* = _true_
	Show the message's text in the notification; off, only who wrote.

# LOG

*[log]*

*level* = _"info"_
	The least important level written to
	_$XDG_STATE_HOME/tarcin/tarcin.log_: *"debug"*, *"info"*, *"warn"* or
	*"error"*. The log never holds message content, names or phone
	numbers.

# KEY BINDINGS

*[keys.*_context_*]*
	Each action of a context takes a list of key sequences. A list
	replaces all bindings of its action, and an empty list unbinds it.
	Keys in a sequence are separated by spaces ("ctrl+x ctrl+e"); a word
	that is not a key name is its letters typed in order ("gg"). No
	binding may be the start of another. *tarcin --dump-keys* shows the
	name of a key you press.

The chat list and the message view fall back to *[keys.navigation]* and
then to *[keys.global]*; the composer falls back to *[keys.global]*. The
contexts, their actions and the defaults:

*[keys.global]*
	*command* = [":"]++
*focus-next* = ["tab"]++
*focus-prev* = ["shift+tab"]++
*help* = ["?"]++
*next-unread* = ["]u"]++
*prev-unread* = ["[u"]++
*quit* = ["ctrl+q"]++
*search* = ["ctrl+k", "/"]

*[keys.navigation]*
	*bottom* = ["G", "end"]++
*down* = ["j", "down"]++
*half-page-down* = ["ctrl+d"]++
*half-page-up* = ["ctrl+u"]++
*page-down* = ["ctrl+f", "pgdown"]++
*page-up* = ["ctrl+b", "pgup"]++
*top* = ["gg", "home"]++
*up* = ["k", "up"]

*[keys.chatlist]*
	*open* = ["enter", "l"]

*[keys.messages]*
	*compose* = ["i", "a"]++
*copy* = ["y"]++
*delete* = ["dd"]++
*edit* = ["e"]++
*forward* = ["f"]++
*jump-quoted* = ["gq"]++
*open-media* = ["o"]++
*react* = ["r"]++
*reply* = ["R"]++
*retry* = ["ctrl+r"]++
*save-media* = ["s"]++
*view-pager* = ["v"]

*[keys.compose]*
	*edit-external* = ["ctrl+x ctrl+e"]++
*newline* = ["shift+enter", "alt+enter", "ctrl+j"]++
*normal-mode* = ["esc"]++
*send* = ["enter"]

# EXAMPLE

```
[privacy]
read_receipts = false

[ui]
theme = "plain"

[media.openers]
"image/*" = ["imv"]

[keys.messages]
reply = ["R", "ctrl+r"]
retry = []
```

# SEE ALSO

*tarcin*(1)
