// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Privacy.ReadReceipts || c.UI.Theme != "auto" || c.LogLevel() != slog.LevelInfo || c.Path() != "" {
		t.Fatalf("defaults = %+v", c)
	}
}

func TestParse(t *testing.T) {
	c, err := Parse("config.toml", []byte(`
[privacy]
read_receipts = false

[ui]
theme = "plain"

[log]
level = "debug"

[notifications]
when = "other-chats"

[keys.messages]
reply = ["R", "ctrl+r"]
delete = []
`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Privacy.ReadReceipts || c.UI.Theme != "plain" || c.LogLevel() != slog.LevelDebug || c.Notify.When != "other-chats" || !c.Notify.Enabled {
		t.Fatalf("config = %+v", c)
	}
	if got := c.Keys["messages"]; !reflect.DeepEqual(got, map[string][]string{"reply": {"R", "ctrl+r"}, "delete": {}}) {
		t.Fatalf("keys = %#v", got)
	}
	// A file that sets only some keys keeps the other defaults.
	c, err = Parse("config.toml", []byte("[log]\nlevel = \"warn\"\n"))
	if err != nil || !c.Privacy.ReadReceipts || c.UI.Theme != "auto" || c.LogLevel() != slog.LevelWarn || c.Notify.When != "unfocused" {
		t.Fatalf("partial config = %+v, %v", c, err)
	}
}

func TestParseErrorsNameFileLineAndKey(t *testing.T) {
	for _, tt := range []struct {
		name, data, want string
	}{
		{"unknown key", "[ui]\ntheme = \"plain\"\nfont = \"mono\"\n", "config.toml:3: ui.font: unknown key"},
		{"unknown table", "\n[privcy]\nread_receipts = false\n", "config.toml:2: privcy"},
		{"wrong type", "[privacy]\nread_receipts = \"no\"\n", "config.toml:2: privacy.read_receipts: incompatible types"},
		{"syntax", "[ui]\ntheme = plain\n", "config.toml:2: "},
		{"bad theme", "[ui]\n\ntheme = \"neon\"\n", `config.toml:3: ui.theme: "neon" is not a theme (want one of "auto", "default", "plain", "high-contrast")`},
		{"bad level", "[log]\nlevel = \"verbose\"\n", `config.toml:2: log.level: "verbose" is not a level`},
		{"bad when", "[notifications]\nwhen = \"always\"\n", `config.toml:2: notifications.when: "always" is not a choice (want one of "unfocused", "other-chats")`},
		{"keys shape", "[keys]\nquit = \"ctrl+q\"\n", "config.toml:2: keys.quit: must be a table of actions"},
		{"binding shape", "[keys.global]\nquit = \"ctrl+q\"\n", "config.toml:2: keys.global.quit: incompatible types"},
		{"binding type", "[keys.global]\nquit = [1]\n", "config.toml:2: keys.global.quit: incompatible types"},
		{"table shape", "privacy = false\n", "config.toml:1: privacy: type mismatch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("config.toml", []byte(tt.data))
			var ce *Error
			if !errors.As(err, &ce) || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to start with %q", err, tt.want)
			}
		})
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("a directory is not a config")
	}
}

func TestKeyError(t *testing.T) {
	c, err := Parse("/home/me/.config/tarcin/config.toml", []byte("[keys.global]\nquit = [\"ctrl+q\"]\n\n[keys.messages]\nreply = [\"gg\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := c.KeyError(errors.New(`keys.messages.reply: "gg" conflicts with top ("g g")`)).Error()
	if got != `/home/me/.config/tarcin/config.toml:5: keys.messages.reply: "gg" conflicts with top ("g g")` {
		t.Fatalf("got %q", got)
	}
	got = c.KeyError(errors.New(`keys.messages: "g q" is bound to both jump-quoted and reply`)).Error()
	if !strings.HasPrefix(got, "/home/me/.config/tarcin/config.toml:5: keys.messages.reply: ") {
		t.Fatalf("a context error names the file's binding: %q", got)
	}
	got = c.KeyError(errors.New(`keys: "q" (global.quit) is a prefix of a binding in messages`)).Error()
	if !strings.HasPrefix(got, "/home/me/.config/tarcin/config.toml:2: keys.global.quit: ") {
		t.Fatalf("a chain error names the file's binding: %q", got)
	}
	got = c.KeyError(errors.New(`keys.compose: "x" is bound to both send and newline`)).Error()
	if !strings.HasPrefix(got, "/home/me/.config/tarcin/config.toml: keys.compose: ") {
		t.Fatalf("an error about defaults only stays at the context: %q", got)
	}
	got = c.KeyError(errors.New(`keys: unknown context "mesages"`)).Error()
	if !strings.HasPrefix(got, "/home/me/.config/tarcin/config.toml: keys: unknown context") {
		t.Fatalf("got %q", got)
	}
	d := Default()
	if got := d.KeyError(errors.New("keys.global.quit: empty binding")).Error(); !strings.HasPrefix(got, "the default config: keys.global.quit") {
		t.Fatalf("got %q", got)
	}
}

func TestWriteReadsBack(t *testing.T) {
	keys := []KeyContext{
		{Name: "global", Actions: []KeyAction{{Name: "quit", Keys: []string{"ctrl+q"}}, {Name: "help", Keys: []string{"?"}}}},
		{Name: "compose", Actions: []KeyAction{{Name: "edit-external", Keys: []string{"ctrl+x ctrl+e"}}, {Name: "odd", Keys: []string{`"\`}}}},
	}
	def := Default()
	var buf bytes.Buffer
	if err := Write(&buf, &def, keys); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	if c.Privacy != def.Privacy || c.UI != def.UI || c.Log != def.Log || c.Notify != def.Notify {
		t.Fatalf("read back %+v", c)
	}
	want := map[string]map[string][]string{
		"global":  {"quit": {"ctrl+q"}, "help": {"?"}},
		"compose": {"edit-external": {"ctrl+x ctrl+e"}, "odd": {`"\`}},
	}
	if !reflect.DeepEqual(c.Keys, want) {
		t.Fatalf("keys = %#v", c.Keys)
	}
	if !strings.Contains(buf.String(), "# Every key is optional") || c.Path() != path {
		t.Fatalf("output:\n%s", buf.String())
	}
}

func TestLineOf(t *testing.T) {
	doc := []byte("# comment\n[ui]\ntheme = \"x\"\n\n[keys.\"messages\"]\n  reply = []\n[log]\n")
	for key, want := range map[string]int{
		"ui.theme": 3, "ui.other": 2, "keys.messages.reply": 6, "keys.messages.edit": 5, "log": 7, "privacy": 0,
	} {
		if got := lineOf(doc, key); got != want {
			t.Errorf("lineOf(%q) = %d, want %d", key, got, want)
		}
	}
}
