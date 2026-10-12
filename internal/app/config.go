// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/config"
	"github.com/Yaman-yagiz/tarcin/internal/notify"
	"github.com/Yaman-yagiz/tarcin/internal/paths"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
)

// settings are the configuration and what it builds for the UI.
type settings struct {
	cfg    config.Config
	keymap *keymap.Keymap
	theme  *theme.Theme
	notify func(ui.Notification) tea.Cmd // nil: no notifications
	drafts string                        // the profile's draft directory; empty in the demo
}

// loadSettings reads the config at path, or the default config file for
// an empty path, and builds its keymap and theme.
func loadSettings(path string) (*settings, error) {
	if path == "" {
		var err error
		if path, err = ConfigPath(); err != nil {
			return nil, err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	km, err := keymap.New(cfg.Keys)
	if err != nil {
		return nil, cfg.KeyError(err)
	}
	return &settings{cfg: cfg, keymap: km, theme: themeFor(cfg.UI.Theme, os.Getenv("NO_COLOR"))}, nil
}

// mediaFromConfig returns the media options of the [media] settings, with
// the cache in dir. Without settings (tests), the defaults apply.
func mediaFromConfig(set *settings, dir string) *mediaOptions {
	m := config.Default().Media
	if set != nil {
		m = set.cfg.Media
	}
	save := defaultSaveDir()
	if m.SaveDir != "" {
		save = expandHome(m.SaveDir)
	}
	return &mediaOptions{
		Dir: dir, Limit: int64(m.MaxDownloadMiB) << 20, UploadLimit: int64(m.MaxUploadMiB) << 20, Cap: int64(m.CacheMiB) << 20,
		SaveDir: save, Open: m.Open, Openers: m.Openers,
	}
}

// notifier returns the notification function of the [notifications]
// settings: desktop notifications if a notification service answers,
// else the terminal bell; nil if they are off. The closer ends the bus
// connection.
func notifier(ctx context.Context, set *settings, log *slog.Logger) (func(ui.Notification) tea.Cmd, func()) {
	n := set.cfg.Notify
	if !n.Enabled {
		return nil, func() {}
	}
	d, err := notify.Connect(ctx)
	if err != nil {
		log.Info("no desktop notifications; ringing the bell", "err", err)
		return func(ui.Notification) tea.Cmd { return tea.Raw("\a") }, func() {}
	}
	return func(nt ui.Notification) tea.Cmd {
		summary, body := nt.Title, nt.Text
		if nt.Sender != "" {
			summary = nt.Sender + " in " + nt.Title
		}
		if !n.Preview {
			body = "New message"
		}
		key := strconv.FormatInt(int64(nt.Chat), 10)
		return func() tea.Msg {
			if err := d.Notify(context.Background(), key, summary, body); err != nil {
				log.Info("notification not shown", "err", err)
			}
			return nil
		}
	}, func() { _ = d.Close() }
}

// themeFor returns the theme ui.theme names; "auto" respects NO_COLOR
// (https://no-color.org).
func themeFor(name, noColor string) *theme.Theme {
	switch {
	case name == "high-contrast":
		return theme.HighContrast()
	case name == "plain", name == "auto" && noColor != "":
		return theme.Plain()
	}
	return theme.Default()
}

// ConfigPath returns the path of the config file.
func ConfigPath() (string, error) {
	p, err := paths.Default(paths.DefaultProfile)
	if err != nil {
		return "", err
	}
	return p.ConfigFile(), nil
}

// ErrUnknownConfigCommand is returned for a config subcommand that does
// not exist.
var ErrUnknownConfigCommand = errors.New("unknown config command")

// ConfigCommands are the subcommands of "tarcin config".
var ConfigCommands = []string{"check", "path", "print-defaults"}

// Config runs "tarcin config <command>" for the config at path (empty:
// the default file): check validates it, path prints where it is, and
// print-defaults prints a complete, commented default config.
func Config(command, path string, out io.Writer) error {
	switch command {
	case "print-defaults":
		def := config.Default()
		return config.Write(out, &def, defaultKeys())
	case "path":
		if path == "" {
			var err error
			if path, err = ConfigPath(); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(out, path)
		return err
	case "check":
		s, err := loadSettings(path)
		if err != nil {
			return err
		}
		if s.cfg.Path() == "" {
			_, err = fmt.Fprintln(out, "No config file: tarcin uses the defaults (see tarcin config print-defaults).")
			return err
		}
		_, err = fmt.Fprintf(out, "%s is valid.\n", s.cfg.Path())
		return err
	}
	return fmt.Errorf("%w %q", ErrUnknownConfigCommand, command)
}

// defaultKeys lists the default bindings in the order of the contexts,
// each context's actions by name.
func defaultKeys() []config.KeyContext {
	specs := keymap.Defaults()
	out := make([]config.KeyContext, 0, len(keymap.Contexts))
	for _, c := range keymap.Contexts {
		kc := config.KeyContext{Name: string(c)}
		for _, a := range slices.Sorted(maps.Keys(specs[c])) {
			kc.Actions = append(kc.Actions, config.KeyAction{Name: string(a), Keys: specs[c][a]})
		}
		out = append(out, kc)
	}
	return out
}
