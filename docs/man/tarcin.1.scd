tarcin(1)

# NAME

tarcin - a terminal client for WhatsApp

# SYNOPSIS

*tarcin* [_options_]

*tarcin* *logout* [*--profile* _name_]

*tarcin* *backup* [*--profile* _name_] [*--with-keys*] _file_

*tarcin* *config* *check*|*path*|*print-defaults* [*--config* _file_]

# DESCRIPTION

tarcin is an unofficial WhatsApp client for the terminal. It links to
your account as a companion device, like WhatsApp Web, and keeps your
chats in a local database, so that it starts at once and works with the
history it has while offline.

Without a command, tarcin opens your chats. The first time, it explains
what it stores and the risk of using an unofficial client, and then
links this device: scan the QR code with WhatsApp on your phone (Linked
devices), or press *p* to link with a pairing code instead.

tarcin is not made or endorsed by WhatsApp or Meta. WhatsApp's terms do
not allow unofficial clients, and an account that uses one can be
restricted. tarcin sends nothing that the official clients would not.

# OPTIONS

*--profile* _name_
	The profile: one account, with its own data. The default is
	*default*. Names are 1 to 32 lowercase letters, digits, *-* or *\_*.

*--config* _file_
	Read the configuration from _file_ instead of
	_$XDG_CONFIG_HOME/tarcin/config.toml_. See *tarcin*(5).

*--demo*
	Run with simulated chats: no account or network is needed, and
	nothing is kept.

*--demo-rate* _n_
	Live messages per second in the demo. The default is 2.

*--demo-link*
	Start the demo as a device that is not linked yet, to try the link
	screen.

*--dump-keys*
	Show the names of the keys you press, for writing key bindings.

*--version*
	Print version information and exit.

*--pprof* _address_
	For developers: serve Go's profiles on _address_, which must be a
	loopback address with a port, such as 127.0.0.1:6060. Other users of
	this computer can reach it too.

*--trace* _file_
	For developers: record an execution trace into _file_, which must
	not exist.

# COMMANDS

*logout*
	Unlink this device from your account, so that your phone no longer
	lists it, and delete its keys. You are then asked whether to delete
	your messages and media too; the default is to keep them. If
	WhatsApp cannot be reached, tarcin asks before deleting the keys.

*backup* [*--with-keys*] _file_
	Write the profile's chats, messages, contacts and configuration to
	_file_, a new gzipped tar archive; an existing file is never
	replaced. It works while tarcin runs. Media are left out, since they
	can be downloaded again, and so are the device's keys unless
	*--with-keys* is given: anyone with them can read your new messages.
	The archive's README says how to restore it.

*config check*
	Validate the configuration file. Errors name the file, the line and
	the key.

*config path*
	Print the path of the configuration file.

*config print-defaults*
	Print a complete configuration with the default values and comments,
	ready to be saved as _config.toml_.

# USING TARCIN

The screen shows the chat list, the open chat and the composer. *tab*
moves the focus between them, *?* shows every key and command, and
*ctrl+q* quits.

In the chat list and the messages, *j* and *k* move, *gg* and *G* go to
the top and the bottom, and *enter* opens a chat. On a selected message:

|[ *i*, *a*
:[ write a message
|  *R*
:[ reply
|  *r*
:[ react
|  *e*
:[ edit your message (within 20 minutes)
|  *dd*
:[ delete for everyone, after asking
|  *f*
:[ forward
|  *y*
:[ copy the text (OSC 52)
|  *v*
:[ read the text in *$PAGER*
|  *o*, *s*
:[ open or save the media
|  *gq*
:[ jump to the quoted message
|  *ctrl+r*
:[ send a failed message again

In the composer, *enter* sends, *shift+enter*, *alt+enter* or *ctrl+j*
starts a new line, *ctrl+x ctrl+e* edits the message in *$EDITOR*, and
*esc* goes back to the messages. *]u* and *[u* go to the next and the
previous chat with unread messages. Every key can be changed; see
*tarcin*(5).

*:* opens the command line:

|[ *:open* _name_
:[ open the first chat whose name matches
|  *:new* _name_, *:new* _+number_
:[ start a chat with a contact (*tab* completes the name) or a phone number
|  *:search* _text_
:[ filter the chat list (also */* or *ctrl+k*)
|  *:find* _words_, *:findin* _words_
:[ search the messages of all chats, or of the open one
|  *:attach* _path_
:[ send a file, with the composer's text as caption
|  *:react* _emoji_
:[ react to the selected message
|  *:members*
:[ show the members of the open group
|  *:mute* [_8h_|_1w_|_always_], *:unmute*
:[ mute the chat
|  *:pin*, *:unpin*
:[ pin the chat to the top (at most three)
|  *:archive*, *:unarchive*, *:archived*
:[ archive the chat, or show the archived chats
|  *:delete-local*
:[ delete the chat and its messages from this computer only
|  *:reconnect*
:[ connect to WhatsApp again now
|  *:debug*
:[ show queues, caches, frames and goroutines
|  *:help*, *:quit*
:[ show the help, or quit

# FILES

_$XDG_CONFIG_HOME/tarcin/config.toml_
	The configuration; see *tarcin*(5). tarcin never writes it.

_$XDG_DATA_HOME/tarcin/profiles/<name>/session.db_
	The device's keys. Anyone with this file can read your new messages:
	never copy it anywhere you do not trust.

_$XDG_DATA_HOME/tarcin/profiles/<name>/tarcin.db_
	Your chats, messages and contacts.

_$XDG_STATE_HOME/tarcin/tarcin.log_
	The log. It holds no message content, names or phone numbers.

_$XDG_CACHE_HOME/tarcin/profiles/<name>/media/_
	Downloaded media. It is always safe to delete.

Directories are created with mode 0700 and files with mode 0600. The
defaults are _~/.config_, _~/.local/share_, _~/.local/state_ and
_~/.cache_.

# ENVIRONMENT

*XDG_CONFIG_HOME*, *XDG_DATA_HOME*, *XDG_STATE_HOME*, *XDG_CACHE_HOME*
	Where tarcin keeps its files. Empty, unset or relative values fall
	back to the defaults.

*TARCIN_CONFIG_DIR*, *TARCIN_DATA_DIR*, *TARCIN_STATE_DIR*, *TARCIN_CACHE_DIR*
	Replace a whole tarcin directory, for tests and portable setups.
	They must be absolute.

*VISUAL*, *EDITOR*
	The editor for *ctrl+x ctrl+e*.

*PAGER*
	The pager for *v*; *less* without one.

*NO_COLOR*
	With the theme *auto*, use no colors.

# EXIT STATUS

*0*
	Success.

*1*
	An error, which tarcin prints.

*2*
	Wrong usage, such as an unknown flag or command.

# SEE ALSO

*tarcin*(5)

The project: https://github.com/Yaman-yagiz/tarcin
