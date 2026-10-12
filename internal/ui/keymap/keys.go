// SPDX-License-Identifier: GPL-3.0-or-later

package keymap

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// namedKeys are the key names Bubble Tea's Key.String produces for
// non-printable keys, plus accepted aliases.
var namedKeys = func() map[string]string {
	m := map[string]string{
		"escape": "esc", "return": "enter", "pageup": "pgup", "pagedown": "pgdown",
		"del": "delete", "ins": "insert", "bs": "backspace",
	}
	for _, k := range []string{
		"enter", "tab", "backspace", "esc", "space", "up", "down", "left", "right",
		"begin", "find", "insert", "delete", "select", "pgup", "pgdown", "home", "end",
	} {
		m[k] = k
	}
	for i := 1; i <= 24; i++ {
		f := fmt.Sprintf("f%d", i)
		m[f] = f
	}
	return m
}()

// modifierOrder is the order in which Bubble Tea prints modifiers.
var modifierOrder = []string{"ctrl", "alt", "shift", "meta", "hyper", "super"}

// Canonical converts one key spec from the configuration ("Ctrl+Q",
// "alt+ctrl+x", "shift+g", "escape") into the exact string Bubble Tea's
// KeyPressMsg.String reports for that key ("ctrl+shift+q", "ctrl+alt+x",
// "G", "esc"), so configured keys and received keys compare equal.
func Canonical(spec string) (string, error) {
	if spec == "" {
		return "", fmt.Errorf("empty key")
	}
	parts := strings.Split(spec, "+")
	if strings.HasSuffix(spec, "++") || spec == "+" { // the plus key itself
		parts = append(parts[:len(parts)-2], "+")
	}
	base := parts[len(parts)-1]
	mods := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		mod := strings.ToLower(p)
		if !slices.Contains(modifierOrder, mod) {
			return "", fmt.Errorf("key %q: unknown modifier %q", spec, p)
		}
		mods[mod] = true
	}
	if base == "" {
		return "", fmt.Errorf("key %q: missing key after modifier", spec)
	}

	if name, ok := namedKeys[strings.ToLower(base)]; ok {
		base = name
	} else {
		r, size := utf8.DecodeRuneInString(base)
		if size != len(base) || !unicode.IsPrint(r) || r == ' ' {
			return "", fmt.Errorf("key %q: %q is neither a key name nor a single printable character", spec, base)
		}
		switch {
		case len(mods) == 0:
			return base, nil // printable keys are reported by their text
		case len(mods) == 1 && mods["shift"] && unicode.IsLetter(r):
			return string(unicode.ToUpper(r)), nil // shift+g is reported as "G"
		case unicode.IsUpper(r):
			mods["shift"] = true // ctrl+G is ctrl+shift+g
			base = string(unicode.ToLower(r))
		}
	}

	var b strings.Builder
	for _, m := range modifierOrder {
		if mods[m] {
			b.WriteString(m)
			b.WriteByte('+')
		}
	}
	b.WriteString(base)
	return b.String(), nil
}

// ParseSequence parses a binding: space-separated keys, where a token
// that is not a key name and has several characters stands for those
// characters typed in order ("gg", "]u"). "ctrl+x ctrl+e" is two keys;
// to bind the letters of a key name, separate them ("e n d").
func ParseSequence(spec string) ([]string, error) {
	var seq []string
	for _, tok := range strings.Fields(spec) {
		if _, named := namedKeys[strings.ToLower(tok)]; !named && !strings.Contains(tok, "+") && utf8.RuneCountInString(tok) > 1 {
			for _, r := range tok {
				k, err := Canonical(string(r))
				if err != nil {
					return nil, fmt.Errorf("binding %q: %w", spec, err)
				}
				seq = append(seq, k)
			}
			continue
		}
		k, err := Canonical(tok)
		if err != nil {
			return nil, fmt.Errorf("binding %q: %w", spec, err)
		}
		seq = append(seq, k)
	}
	if len(seq) == 0 {
		return nil, fmt.Errorf("empty binding")
	}
	return seq, nil
}

// IsText reports whether a canonical key types text (a printable
// character or space without ctrl/alt), which a text input must receive.
func IsText(key string) bool {
	if key == "space" {
		return true
	}
	r, size := utf8.DecodeRuneInString(key)
	return size == len(key) && unicode.IsPrint(r)
}
