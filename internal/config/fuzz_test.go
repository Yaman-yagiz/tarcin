// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// unsafeRune reports a rune that must not reach the terminal in an error.
func unsafeRune(r rune) bool {
	return (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// keyContexts lists c's key bindings for Write, sorted.
func keyContexts(c *Config) []KeyContext {
	var out []KeyContext
	for _, ctx := range slices.Sorted(maps.Keys(c.Keys)) {
		kc := KeyContext{Name: ctx}
		for _, a := range slices.Sorted(maps.Keys(c.Keys[ctx])) {
			kc.Actions = append(kc.Actions, KeyAction{Name: a, Keys: c.Keys[ctx][a]})
		}
		out = append(out, kc)
	}
	return out
}

// normalize drops what does not change a config's meaning: its source,
// and empty tables, which read back as none.
func normalize(c *Config) {
	c.data = nil
	for ctx, actions := range c.Keys {
		if len(actions) == 0 {
			delete(c.Keys, ctx)
		}
	}
	if len(c.Keys) == 0 {
		c.Keys = nil
	}
	if len(c.Media.Openers) == 0 {
		c.Media.Openers = nil
	}
}

// FuzzParse parses any config file. It must not panic; an error names
// the file and carries no terminal controls, since a config may come
// from someone else's dotfiles; and a valid config written by Write
// parses back to the same settings.
func FuzzParse(f *testing.F) {
	var def strings.Builder
	d := Default()
	if err := Write(&def, &d, []KeyContext{{Name: "messages", Actions: []KeyAction{{Name: "reply", Keys: []string{"R"}}}}}); err != nil {
		f.Fatal(err)
	}
	for _, s := range []string{
		def.String(), "", "[privacy]\nread_receipts = 1\n", "[ui]\ntheme = \"\x1b[31mred\"\n", "[keys.\"\x1b]52;c;x\x07\"]\nquit = [\"q\"]\n",
		"[media]\nmax_download_mib = 99999999999999999999\n", "[media.openers]\n\"image/*\" = [\"feh\"]\n", "a = [[[[[",
		"[notifications]\nenabled = false\npreview = false\n[log]\nlevel = \"debug\"\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		c, err := Parse("config.toml", []byte(data))
		if err != nil {
			msg := err.Error()
			if !strings.HasPrefix(msg, "config.toml") {
				t.Fatalf("the error %q does not name the file", msg)
			}
			if strings.ContainsFunc(msg, unsafeRune) {
				t.Fatalf("the error %q carries terminal controls", msg)
			}
			return
		}
		var out strings.Builder
		if err := Write(&out, &c, keyContexts(&c)); err != nil {
			t.Fatal(err)
		}
		back, err := Parse("config.toml", []byte(out.String()))
		if err != nil {
			t.Fatalf("Write's output does not parse: %v\n%s", err, out.String())
		}
		normalize(&back)
		normalize(&c)
		if !reflect.DeepEqual(back, c) {
			t.Fatalf("round trip changed the config:\n%+v\n%+v", c, back)
		}
	})
}
