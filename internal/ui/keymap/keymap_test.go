// SPDX-License-Identifier: GPL-3.0-or-later

package keymap

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestCanonical(t *testing.T) {
	tests := []struct{ in, want string }{
		{"q", "q"},
		{"G", "G"},
		{"?", "?"},
		{"ş", "ş"},
		{"ctrl+q", "ctrl+q"},
		{"Ctrl+q", "ctrl+q"},
		{"ctrl+Q", "ctrl+shift+q"},
		{"alt+ctrl+x", "ctrl+alt+x"},
		{"shift+g", "G"},
		{"shift+tab", "shift+tab"},
		{"escape", "esc"},
		{"Esc", "esc"},
		{"return", "enter"},
		{"alt+enter", "alt+enter"},
		{"PageDown", "pgdown"},
		{"space", "space"},
		{"f12", "f12"},
		{"super+shift+ctrl+alt+k", "ctrl+alt+shift+super+k"},
		{"+", "+"},
		{"ctrl++", "ctrl++"},
	}
	for _, tt := range tests {
		got, err := Canonical(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "ctrl+", "foo+x", "ab", " ", "ctrl+ab", "\x01"} {
		if got, err := Canonical(bad); err == nil {
			t.Errorf("Canonical(%q) = %q, want an error", bad, got)
		}
	}
}

// TestCanonicalMatchesBubbleTea proves configured names compare equal to
// the names Bubble Tea reports for real key presses.
func TestCanonicalMatchesBubbleTea(t *testing.T) {
	keys := map[string]tea.KeyPressMsg{
		"ctrl+q":       {Code: 'q', Mod: tea.ModCtrl},
		"G":            {Code: 'g', ShiftedCode: 'G', Mod: tea.ModShift, Text: "G"},
		"?":            {Code: '?', Text: "?"},
		"enter":        {Code: tea.KeyEnter},
		"esc":          {Code: tea.KeyEscape},
		"tab":          {Code: tea.KeyTab},
		"shift+tab":    {Code: tea.KeyTab, Mod: tea.ModShift},
		"alt+enter":    {Code: tea.KeyEnter, Mod: tea.ModAlt},
		"shift+enter":  {Code: tea.KeyEnter, Mod: tea.ModShift},
		"space":        {Code: tea.KeySpace, Text: " "},
		"pgdown":       {Code: tea.KeyPgDown},
		"ctrl+shift+q": {Code: 'q', Mod: tea.ModCtrl | tea.ModShift},
		"ctrl+alt+x":   {Code: 'x', Mod: tea.ModCtrl | tea.ModAlt},
		"f5":           {Code: tea.KeyF5},
	}
	for spec, msg := range keys {
		c, err := Canonical(spec)
		if err != nil {
			t.Fatalf("Canonical(%q): %v", spec, err)
		}
		if c != msg.String() {
			t.Errorf("Canonical(%q) = %q, but Bubble Tea reports %q", spec, c, msg.String())
		}
	}
}

func TestParseSequence(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"gg", []string{"g", "g"}},
		{"]u", []string{"]", "u"}},
		{"ctrl+x ctrl+e", []string{"ctrl+x", "ctrl+e"}},
		{"end", []string{"end"}},
		{"e n d", []string{"e", "n", "d"}},
		{"  dd  ", []string{"d", "d"}},
		{"g ctrl+j", []string{"g", "ctrl+j"}},
	}
	for _, tt := range tests {
		got, err := ParseSequence(tt.in)
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("ParseSequence(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	for _, bad := range []string{"", "   ", "ctrl+x foo+y"} {
		if _, err := ParseSequence(bad); err == nil {
			t.Errorf("ParseSequence(%q): want an error", bad)
		}
	}
}

func TestDefaults(t *testing.T) {
	km := Default()
	seen := map[Action]bool{}
	for _, b := range km.Bindings() {
		seen[b.Action] = true
		if b.String() == "" {
			t.Fatalf("empty binding for %s", b.Action)
		}
	}
	for c, actions := range defaults {
		for a := range actions {
			if !seen[a] {
				t.Errorf("%s.%s has no binding", c, a)
			}
		}
	}
}

// feed sends a sequence of key specs and returns the last result.
func feed(t *testing.T, m *Matcher, c Context, specs ...string) Result {
	t.Helper()
	var r Result
	for _, s := range specs {
		k, err := Canonical(s)
		if err != nil {
			t.Fatal(err)
		}
		r = m.Feed(c, k)
	}
	return r
}

func TestMatcher(t *testing.T) {
	tests := []struct {
		name    string
		ctx     Context
		keys    []string
		action  Action
		pending bool
	}{
		{"single key", ChatList, []string{"j"}, Down, false},
		{"arrow alias", Messages, []string{"down"}, Down, false},
		{"chat list open", ChatList, []string{"enter"}, Open, false},
		{"sequence prefix", Messages, []string{"g"}, "", true},
		{"sequence gg", Messages, []string{"g", "g"}, Top, false},
		{"sequence gq", Messages, []string{"g", "q"}, JumpQuoted, false},
		{"sequence dd", Messages, []string{"d", "d"}, Delete, false},
		{"broken sequence retries the key", Messages, []string{"g", "j"}, Down, false},
		{"broken sequence of unbound keys", Messages, []string{"d", "x"}, "", false},
		{"gq is not in the chat list", ChatList, []string{"g", "q"}, "", false},
		{"global from a list", ChatList, []string{"?"}, Help, false},
		{"global sequence", ChatList, []string{"]", "u"}, NextUnread, false},
		{"composer types ?", Compose, []string{"?"}, "", false},
		{"composer types j", Compose, []string{"j"}, "", false},
		{"composer types ]", Compose, []string{"]"}, "", false},
		{"composer keeps ctrl globals", Compose, []string{"ctrl+q"}, Quit, false},
		{"composer send", Compose, []string{"enter"}, Send, false},
		{"composer newline", Compose, []string{"alt+enter"}, Newline, false},
		{"composer external editor", Compose, []string{"ctrl+x", "ctrl+e"}, EditExternal, false},
		{"composer escape", Compose, []string{"esc"}, NormalMode, false},
		{"composer tab moves focus", Compose, []string{"tab"}, FocusNext, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMatcher(Default())
			r := feed(t, m, tt.ctx, tt.keys...)
			if r.Action != tt.action || r.Pending != tt.pending {
				t.Fatalf("got %+v, want action %q pending %v", r, tt.action, tt.pending)
			}
			if r.Handled() != (tt.action != "" || tt.pending) {
				t.Fatal("Handled is inconsistent")
			}
			if !tt.pending && len(m.Pending()) != 0 {
				t.Fatalf("pending keys left behind: %q", m.Pending())
			}
		})
	}
	m := NewMatcher(Default())
	feed(t, m, Messages, "g")
	if !slices.Equal(m.Pending(), []string{"g"}) {
		t.Fatalf("Pending() = %q", m.Pending())
	}
	m.Reset()
	if len(m.Pending()) != 0 {
		t.Fatal("Reset did not clear pending keys")
	}
}

func TestOverrides(t *testing.T) {
	km, err := New(map[string]map[string][]string{
		"navigation": {"down": {"n", "ctrl+n"}},
		"messages":   {"delete": {}},
		"compose":    {"send": {"ctrl+s"}, "newline": {"enter"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := NewMatcher(km)
	if r := feed(t, m, ChatList, "j"); r.Handled() {
		t.Error("j must be unbound after overriding down")
	}
	if r := feed(t, m, ChatList, "ctrl+n"); r.Action != Down {
		t.Errorf("ctrl+n = %+v, want down", r)
	}
	if r := feed(t, m, Messages, "d"); r.Handled() {
		t.Error("dd must be unbound")
	}
	if r := feed(t, m, Compose, "enter"); r.Action != Newline {
		t.Errorf("enter in compose = %+v, want newline", r)
	}
}

func TestOverrideErrors(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]map[string][]string
		want      string
	}{
		{"unknown context", map[string]map[string][]string{"nope": {"quit": {"q"}}}, `unknown context "nope"`},
		{"unknown action", map[string]map[string][]string{"global": {"fly": {"f"}}}, `unknown action "fly"`},
		{"action in wrong context", map[string]map[string][]string{"global": {"send": {"ctrl+s"}}}, `unknown action "send"`},
		{"invalid key", map[string]map[string][]string{"global": {"quit": {"hyperdrive+q"}}}, "unknown modifier"},
		{"duplicate binding", map[string]map[string][]string{"navigation": {"up": {"j"}}}, "bound to both"},
		{"prefix in one context", map[string]map[string][]string{"messages": {"edit": {"d"}}}, "prefix"},
		{"prefix across the chain", map[string]map[string][]string{"global": {"help": {"g"}}}, "prefix"},
		{"composer text key", map[string]map[string][]string{"compose": {"send": {"s"}}}, "text key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.overrides)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestIsText(t *testing.T) {
	for k, want := range map[string]bool{"a": true, "?": true, "ş": true, "space": true, "G": true, "enter": false, "ctrl+a": false, "alt+enter": false, "f1": false} {
		if IsText(k) != want {
			t.Errorf("IsText(%q) = %v, want %v", k, !want, want)
		}
	}
}

func TestDumpModel(t *testing.T) {
	d := NewDumpModel()
	d.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	d.Update(tea.KeyPressMsg{Code: 'ş', Text: "ş"})
	d.Update(tea.PasteMsg{Content: "hello"})
	view := d.View().Content
	for _, want := range []string{`"ctrl+q"`, `"ş"`, `text="ş"`, "paste of 5 bytes"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	if _, cmd := d.Update(ctrlC); cmd != nil {
		t.Fatal("a single ctrl+c must not quit")
	}
	if _, cmd := d.Update(ctrlC); cmd == nil {
		t.Fatal("a second ctrl+c must quit")
	}
	for range dumpHistory * 2 {
		d.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	}
	if len(d.lines) != dumpHistory {
		t.Fatalf("history holds %d lines, want %d", len(d.lines), dumpHistory)
	}
}

func TestDefaultsAreACopy(t *testing.T) {
	d := Defaults()
	if got := d[Global][Quit]; len(got) != 1 || got[0] != "ctrl+q" {
		t.Fatalf("quit = %q", got)
	}
	d[Global][Quit][0] = "x"
	if Defaults()[Global][Quit][0] != "ctrl+q" {
		t.Fatal("Defaults must not expose the defaults")
	}
	overrides := map[string]map[string][]string{}
	for c, actions := range Defaults() {
		overrides[string(c)] = map[string][]string{}
		for a, specs := range actions {
			overrides[string(c)][string(a)] = specs
		}
	}
	if _, err := New(overrides); err != nil {
		t.Fatalf("the defaults, written as overrides, must be valid: %v", err)
	}
}
