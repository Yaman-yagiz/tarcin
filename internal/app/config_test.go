// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/config"
	"github.com/Yaman-yagiz/tarcin/internal/logging"
	"github.com/Yaman-yagiz/tarcin/internal/paths"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
)

func writeConfig(t *testing.T, p paths.Paths, data string) string {
	t.Helper()
	if err := os.MkdirAll(p.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return p.ConfigFile()
}

func TestPrintDefaultsIsAValidConfig(t *testing.T) {
	var out bytes.Buffer
	if err := Config("print-defaults", "", &out); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, testProfile(t), out.String())
	s, err := loadSettings("")
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if s.cfg.Path() != path || !s.cfg.Privacy.ReadReceipts || s.theme.ID != "default" {
		t.Fatalf("settings = %+v", s.cfg)
	}
	if got, want := len(s.keymap.Bindings()), len(keymap.Default().Bindings()); got != want {
		t.Fatalf("%d bindings, want the %d defaults", got, want)
	}
	for _, want := range []string{"[keys.global]", `quit = ["ctrl+q"]`, `edit-external = ["ctrl+x ctrl+e"]`, "read_receipts = true"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("print-defaults lacks %q:\n%s", want, out.String())
		}
	}
}

func TestConfigCheckAndPath(t *testing.T) {
	p := testProfile(t)
	var out bytes.Buffer
	if err := Config("check", "", &out); err != nil || !strings.Contains(out.String(), "No config file") {
		t.Fatalf("check without a file: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := Config("path", "", &out); err != nil || strings.TrimSpace(out.String()) != p.ConfigFile() {
		t.Fatalf("path = %q, %v", out.String(), err)
	}
	path := writeConfig(t, p, "[keys.messages]\nreply = [\"gq\"]\n")
	err := Config("check", path, io.Discard)
	var ce *config.Error
	if !errors.As(err, &ce) || ce.Line != 2 || ce.Key != "keys.messages.reply" {
		t.Fatalf("a conflicting binding: %v", err)
	}
	if err := Config("nope", "", io.Discard); !errors.Is(err, ErrUnknownConfigCommand) {
		t.Fatal(err)
	}
	if !slices.Equal(ConfigCommands, []string{"check", "path", "print-defaults"}) {
		t.Fatal(ConfigCommands)
	}
}

func TestSettingsApplyTheConfig(t *testing.T) {
	writeConfig(t, testProfile(t), "[privacy]\nread_receipts = false\n\n[ui]\ntheme = \"plain\"\n\n[keys.global]\nquit = [\"ctrl+c\"]\n")
	s, err := loadSettings("")
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg.Privacy.ReadReceipts || s.theme.ID != "plain" {
		t.Fatalf("settings = %+v, theme %s", s.cfg, s.theme.ID)
	}
	quit := slices.IndexFunc(s.keymap.Bindings(), func(b keymap.Binding) bool { return b.Action == keymap.Quit })
	if quit < 0 || s.keymap.Bindings()[quit].String() != "ctrl+c" {
		t.Fatal("the quit binding must be ctrl+c")
	}
	for _, tt := range []struct{ name, noColor, want string }{
		{"auto", "", "default"}, {"auto", "1", "plain"}, {"default", "1", "default"}, {"plain", "", "plain"},
		{"high-contrast", "", "high-contrast"}, {"high-contrast", "1", "high-contrast"},
	} {
		if got := themeFor(tt.name, tt.noColor).ID; got != tt.want {
			t.Errorf("themeFor(%q, NO_COLOR=%q) = %s, want %s", tt.name, tt.noColor, got, tt.want)
		}
	}
}

// TestRunRejectsABadConfig: a bad config stops tarcin before it touches
// the profile.
func TestRunRejectsABadConfig(t *testing.T) {
	p := testProfile(t)
	path := writeConfig(t, p, "[ui]\nfont = \"mono\"\n")
	err := Run(t.Context(), strings.NewReader(""), io.Discard, RunOptions{ConfigPath: path})
	if err == nil || !strings.Contains(err.Error(), "config.toml:2: ui.font: unknown key") {
		t.Fatalf("Run = %v", err)
	}
	if _, err := os.Stat(p.ProfileData()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nothing may be created for a bad config")
	}
	if err := RunDemo(t.Context(), strings.NewReader(""), io.Discard, DemoOptions{ConfigPath: path}); err == nil {
		t.Fatal("the demo must reject it too")
	}
}

func TestNotifierFallsBackToTheBell(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/tarcin-test-bus")
	set := &settings{cfg: config.Default()}
	fn, closeFn := notifier(t.Context(), set, logging.Discard())
	defer closeFn()
	if fn == nil {
		t.Fatal("notifications are on by default")
	}
	if raw, ok := fn(ui.Notification{Chat: 1, Title: "Aile", Text: "x"})().(tea.RawMsg); !ok || raw.Msg != "\a" {
		t.Fatalf("without a bus, the bell rings: %#v", raw)
	}
	set.cfg.Notify.Enabled = false
	if fn, _ := notifier(t.Context(), set, logging.Discard()); fn != nil {
		t.Fatal("off means nothing")
	}
}
