// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
)

// KeyContext is one [keys.<context>] table of Write, in the order given.
type KeyContext struct {
	Name    string
	Actions []KeyAction
}

// KeyAction is an action and its key sequences.
type KeyAction struct {
	Name string
	Keys []string
}

// Write prints c as a complete, commented config.toml, with keys as the
// key bindings. Parse reads it back to c.
func Write(w io.Writer, c *Config, keys []KeyContext) error {
	var b strings.Builder
	b.WriteString(`# tarcin configuration: $XDG_CONFIG_HOME/tarcin/config.toml
# (usually ~/.config/tarcin/config.toml).
#
# Every key is optional; this file lists the defaults. An unknown key, a
# wrong type or a bad value is an error, so a typo never goes unnoticed.
# tarcin never writes this file. "tarcin config check" validates it.

[privacy]
# Tell senders when you have read their messages (blue ticks).
`)
	fmt.Fprintf(&b, "read_receipts = %t\n", c.Privacy.ReadReceipts)
	b.WriteString(`# Show a chat when you are typing in it.
`)
	fmt.Fprintf(&b, "typing = %t\n", c.Privacy.Typing)
	b.WriteString(`# Show your contacts when you are online: only while you use tarcin, from
# a key press until two minutes without one or until the terminal loses
# focus. Off, you also do not see whether they are online.
`)
	fmt.Fprintf(&b, "presence = %t\n", c.Privacy.Presence)
	b.WriteString(`
[ui]
# "default" follows the 16 colors of your terminal's theme; "plain" uses
# none, only bold, underline and reverse; "high-contrast" uses no colors
# and no faint text, and reverses selections and warnings. "auto" is
# "default", or "plain" when the NO_COLOR environment variable is set.
`)
	fmt.Fprintf(&b, "theme = %s\n", tomlString(c.UI.Theme))
	b.WriteString(`
[media]
# Downloads larger than this are refused, by the size the message declares
# and while streaming.
`)
	fmt.Fprintf(&b, "max_download_mib = %d\n", c.Media.MaxDownloadMiB)
	b.WriteString(`# The largest file :attach sends.
`)
	fmt.Fprintf(&b, "max_upload_mib = %d\n", c.Media.MaxUploadMiB)
	b.WriteString(`# The media cache ($XDG_CACHE_HOME/tarcin) keeps at most this much; the
# least recently opened files go first. It is always safe to delete.
`)
	fmt.Fprintf(&b, "cache_mib = %d\n", c.Media.CacheMiB)
	b.WriteString(`# Where s saves media; empty: $XDG_DOWNLOAD_DIR, or ~/Downloads. A saved
# file never replaces another: "name (1).pdf" is used instead.
`)
	fmt.Fprintf(&b, "save_dir = %s\n", tomlString(c.Media.SaveDir))
	b.WriteString(`# The program that opens media (o), with its arguments; the file's path
# is added at the end. It runs without a shell.
`)
	fmt.Fprintf(&b, "open = %s\n", tomlList(c.Media.Open))
	b.WriteString(`# Programs for some MIME types instead, for example:
#   [media.openers]
#   "image/*" = ["imv"]
#   "application/pdf" = ["zathura"]
`)
	if len(c.Media.Openers) > 0 {
		b.WriteString("\n[media.openers]\n")
		for _, mime := range slices.Sorted(maps.Keys(c.Media.Openers)) {
			fmt.Fprintf(&b, "%s = %s\n", tomlString(mime), tomlList(c.Media.Openers[mime]))
		}
	}
	b.WriteString(`
[notifications]
# A desktop notification for each new message while the terminal does
# not have focus: not for muted chats, history or a backlog. Without a
# desktop session the terminal bell rings instead.
`)
	fmt.Fprintf(&b, "enabled = %t\n", c.Notify.Enabled)
	b.WriteString(`# Show the message's text in the notification; off, only who wrote.
`)
	fmt.Fprintf(&b, "preview = %t\n", c.Notify.Preview)
	b.WriteString(`
[log]
# The least important level written to $XDG_STATE_HOME/tarcin/tarcin.log:
# "debug", "info", "warn" or "error". The log never holds message
# content, names or phone numbers.
`)
	fmt.Fprintf(&b, "level = %s\n", tomlString(c.Log.Level))
	b.WriteString(`
# Key bindings, per context. The chat list and the message view fall back
# to [keys.navigation] and then [keys.global]; the composer falls back to
# [keys.global]. A list replaces all bindings of its action, and an empty
# list unbinds it. Keys in a sequence are separated by spaces
# ("ctrl+x ctrl+e"); a word that is not a key name is its letters typed
# in order ("gg"). No binding may be the start of another. Run
# "tarcin --dump-keys" to see the name of a key you press.
`)
	for _, kc := range keys {
		fmt.Fprintf(&b, "\n[keys.%s]\n", tomlKey(kc.Name))
		for _, a := range kc.Actions {
			fmt.Fprintf(&b, "%s = %s\n", tomlKey(a.Name), tomlList(a.Keys))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// tomlString quotes s as a TOML basic string, escaping the characters
// one cannot hold.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlList writes a list of strings.
func tomlList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = tomlString(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// tomlKey writes a key bare where TOML allows it, and quoted elsewhere.
func tomlKey(k string) string {
	bare := k != "" && !strings.ContainsFunc(k, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-'
	})
	if bare {
		return k
	}
	return tomlString(k)
}
