// SPDX-License-Identifier: GPL-3.0-or-later

// Package config reads the user's config.toml (docs/adr/0012-config-format-and-cli.md).
// Validation is strict: an unknown key, a wrong type or a bad value is an
// error that names the file, the line and the key. tarcin never writes
// the file; Write prints the defaults for the user to copy.
package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the user's configuration. Start from Default; Load and Parse
// keep the defaults of every key the file leaves out.
type Config struct {
	Privacy Privacy `toml:"privacy"`
	UI      UI      `toml:"ui"`
	Media   Media   `toml:"media"`
	Notify  Notify  `toml:"notifications"`
	Log     Log     `toml:"log"`
	// Keys overrides key bindings: context → action → key sequences. The
	// UI's keymap validates it; KeyError places its errors in the file.
	Keys map[string]map[string][]string `toml:"keys"`

	path string // the file it was read from; empty for the defaults
	data []byte
}

// Privacy holds what others learn about you.
type Privacy struct {
	// ReadReceipts tells senders when you have read their messages.
	ReadReceipts bool `toml:"read_receipts"`
	// Typing shows a chat when you are typing in it.
	Typing bool `toml:"typing"`
	// Presence shows your contacts when you are online, and lets you see
	// when they are.
	Presence bool `toml:"presence"`
}

// Media holds the settings for attachments.
type Media struct {
	// MaxDownloadMiB is the largest download; larger media is refused.
	MaxDownloadMiB int `toml:"max_download_mib"`
	// MaxUploadMiB is the largest attachment sent.
	MaxUploadMiB int `toml:"max_upload_mib"`
	// CacheMiB caps the media cache; the least recently used files go.
	CacheMiB int `toml:"cache_mib"`
	// SaveDir is where s saves media; empty: $XDG_DOWNLOAD_DIR or
	// ~/Downloads. "~/" expands to the home directory.
	SaveDir string `toml:"save_dir"`
	// Open is the program that opens media, with its arguments; the
	// file's path is added at the end.
	Open []string `toml:"open"`
	// Openers replace Open for MIME types: "image/*" or "application/pdf".
	Openers map[string][]string `toml:"openers"`
}

// Notify holds the settings of desktop notifications.
type Notify struct {
	// Enabled shows a notification for a new message while the terminal
	// does not have focus, or rings the bell without a desktop.
	Enabled bool `toml:"enabled"`
	// Preview puts the message's text in the notification; off, it says
	// only who wrote.
	Preview bool `toml:"preview"`
}

// UI holds the interface's settings.
type UI struct {
	Theme string `toml:"theme"` // one of Themes
}

// Log holds the log's settings.
type Log struct {
	Level string `toml:"level"` // one of Levels
}

// Themes are the values of ui.theme. "auto" is "default", or "plain"
// when NO_COLOR is set.
var Themes = []string{"auto", "default", "plain", "high-contrast"}

// Levels are the values of log.level.
var Levels = []string{"debug", "info", "warn", "error"}

// Default returns the defaults.
func Default() Config {
	return Config{
		Privacy: Privacy{ReadReceipts: true, Typing: true, Presence: true},
		UI:      UI{Theme: "auto"},
		Media:   Media{MaxDownloadMiB: 100, MaxUploadMiB: 100, CacheMiB: 2048, Open: []string{"xdg-open"}},
		Notify:  Notify{Enabled: true, Preview: true},
		Log:     Log{Level: "info"},
	}
}

// Path returns the file the config was read from, or "" for the defaults.
func (c *Config) Path() string { return c.path }

// Load reads the config at path. A missing file gives the defaults.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the user's config path
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading the config: %w", err)
	}
	return Parse(path, data)
}

// Error is a problem in a config file.
type Error struct {
	Path string
	Line int    // 0 if unknown
	Key  string // the key path, such as "ui.theme"; may be empty
	Msg  string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(printable(e.Path))
	if e.Line > 0 {
		b.WriteString(":" + strconv.Itoa(e.Line))
	}
	b.WriteString(": ")
	if e.Key != "" {
		b.WriteString(printable(e.Key) + ": ")
	}
	b.WriteString(printable(e.Msg))
	return b.String()
}

// printable escapes what a terminal would act on: an error quotes the
// file, which may come from someone else's dotfiles.
func printable(s string) string {
	if !strings.ContainsFunc(s, controlRune) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if controlRune(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// controlRune reports a control, bidi or line-separator character.
func controlRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) ||
		(r >= 0x2066 && r <= 0x2069) || r == 0x2028 || r == 0x2029
}

// typeError matches BurntSushi's type errors, which carry the line and
// key in their text.
var typeError = regexp.MustCompile(`^toml: (?:line (\d+) )?\(last key "([^"]*)"\): (.*)$`)

// Parse decodes data, read from path, over the defaults and validates it.
func Parse(path string, data []byte) (Config, error) {
	c := Default()
	c.path, c.data = path, data
	md, err := toml.Decode(string(data), &c)
	if err != nil {
		var pe toml.ParseError
		if errors.As(err, &pe) {
			return Config{}, &Error{Path: path, Line: pe.Position.Line, Key: pe.LastKey, Msg: pe.Message}
		}
		if m := typeError.FindStringSubmatch(err.Error()); m != nil {
			line, _ := strconv.Atoi(m[1])
			return Config{}, &Error{Path: path, Line: line, Key: m[2], Msg: m[3]}
		}
		return Config{}, &Error{Path: path, Msg: strings.TrimPrefix(err.Error(), "toml: ")}
	}
	if keys := md.Undecoded(); len(keys) > 0 {
		k := keys[0].String()
		return Config{}, c.errorf(k, "unknown key")
	}
	if err := c.checkShapes(&md); err != nil {
		return Config{}, err
	}
	if !slices.Contains(Themes, c.UI.Theme) {
		return Config{}, c.errorf("ui.theme", "%q is not a theme (want one of %s)", c.UI.Theme, quoted(Themes))
	}
	if !slices.Contains(Levels, c.Log.Level) {
		return Config{}, c.errorf("log.level", "%q is not a level (want one of %s)", c.Log.Level, quoted(Levels))
	}
	if err := c.checkMedia(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// checkMedia validates the [media] settings.
func (c *Config) checkMedia() error {
	m := &c.Media
	switch {
	case m.MaxDownloadMiB < 1 || m.MaxDownloadMiB > 4096:
		return c.errorf("media.max_download_mib", "%d is not between 1 and 4096", m.MaxDownloadMiB)
	case m.MaxUploadMiB < 1 || m.MaxUploadMiB > 2048:
		return c.errorf("media.max_upload_mib", "%d is not between 1 and 2048", m.MaxUploadMiB)
	case m.CacheMiB < 16:
		return c.errorf("media.cache_mib", "%d is below 16", m.CacheMiB)
	case len(m.Open) == 0 || m.Open[0] == "":
		return c.errorf("media.open", "a program is needed, such as [\"xdg-open\"]")
	case m.SaveDir != "" && !strings.HasPrefix(m.SaveDir, "/") && !strings.HasPrefix(m.SaveDir, "~/"):
		return c.errorf("media.save_dir", "%q must be absolute or start with ~/", m.SaveDir)
	}
	for _, mime := range slices.Sorted(maps.Keys(m.Openers)) {
		argv := m.Openers[mime]
		if !strings.Contains(mime, "/") {
			return c.errorf("media.openers", "%q is not a MIME type such as \"image/*\"", mime)
		}
		if len(argv) == 0 || argv[0] == "" {
			return c.errorf("media.openers", "%q needs a program", mime)
		}
	}
	return nil
}

// checkShapes rejects what the decoder accepts in the wrong shape: a
// value where a [keys.<context>] table belongs becomes an empty table.
// An implicit table, such as keys in [keys.global], has no type.
func (c *Config) checkShapes(md *toml.MetaData) error {
	for _, ctx := range slices.Sorted(maps.Keys(c.Keys)) {
		if t := md.Type("keys", ctx); t != "Hash" && t != "" {
			return c.errorf("keys."+ctx, "must be a table of actions, such as [keys.%s]", ctx)
		}
	}
	return nil
}

// LogLevel returns log.level as a slog level.
func (c *Config) LogLevel() slog.Level {
	switch c.Log.Level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// keyPath matches the key path at the start of a keymap error, such as
// "keys.messages.reply: …".
var keyPath = regexp.MustCompile(`^(keys(?:\.[\w-]+)*): (.*)$`)

// KeyError places an error of the keymap built from Keys in the file:
// at the binding of the file it names, else at its context's table.
func (c *Config) KeyError(err error) error {
	key, msg := "keys", err.Error()
	if m := keyPath.FindStringSubmatch(msg); m != nil {
		key, msg = m[1], m[2]
	}
	if binding := c.namedBinding(key, msg); binding != "" {
		key = binding
	}
	return c.errorf(key, "%s", msg)
}

// namedBinding returns the one binding of the file that an error about
// key names, as "keys.<context>.<action>", or "". An error about a
// context names its actions bare; one about the whole keymap names them
// as "<context>.<action>".
func (c *Config) namedBinding(key, msg string) string {
	parts := strings.Split(key, ".")
	if len(parts) > 2 {
		return ""
	}
	var hits []string
	for ctx, actions := range c.Keys {
		if len(parts) == 2 && parts[1] != ctx {
			continue
		}
		for action := range actions {
			name := action
			if len(parts) == 1 {
				name = ctx + "." + action
			}
			if mentions(msg, name) {
				hits = append(hits, "keys."+ctx+"."+action)
			}
		}
	}
	if len(hits) != 1 {
		return ""
	}
	return hits[0]
}

// mentions reports whether msg contains name as a whole word.
func mentions(msg, name string) bool {
	for i := 0; i < len(msg); {
		j := strings.Index(msg[i:], name)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(name)
		if (j == 0 || !isNameByte(msg[j-1])) && (end == len(msg) || !isNameByte(msg[end])) {
			return true
		}
		i = j + 1
	}
	return false
}

func isNameByte(b byte) bool {
	return b == '-' || b == '_' || b == '.' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9'
}

func (c *Config) errorf(key, format string, args ...any) error {
	path := c.path
	if path == "" {
		path = "the default config"
	}
	return &Error{Path: path, Line: lineOf(c.data, key), Key: key, Msg: fmt.Sprintf(format, args...)}
}

func quoted(values []string) string {
	q := make([]string, len(values))
	for i, v := range values {
		q[i] = strconv.Quote(v)
	}
	return strings.Join(q, ", ")
}

// lineOf finds the line that defines key (a dotted path) in a TOML
// document, or the line of the nearest table that contains it; 0 if none.
// It understands what a config holds: table headers and key = value
// lines, with bare or quoted keys.
func lineOf(data []byte, key string) int {
	want := strings.Split(key, ".")
	var table []string
	best, bestLen := 0, 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		var path []string
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "["):
			end := strings.LastIndex(line, "]")
			if end < 0 {
				continue
			}
			table = splitKey(strings.Trim(line[:end], "[] "))
			path = table
		default:
			name, _, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			path = append(slices.Clone(table), splitKey(name)...)
		}
		if l := commonPrefix(path, want); l == len(path) && l > bestLen {
			best, bestLen = n, l
			if l == len(want) {
				return n
			}
		}
	}
	return best
}

func splitKey(s string) []string {
	parts := strings.Split(s, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return parts
}

func commonPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
