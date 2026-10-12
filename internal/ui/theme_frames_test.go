// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
)

var sgrSequence = regexp.MustCompile("\x1b\\[([0-9;:]*)m")

// colorOrFaint names an SGR parameter list's color or faint attribute,
// or "".
func colorOrFaint(params string) string {
	for p := range strings.FieldsFuncSeq(params, func(r rune) bool { return r == ';' || r == ':' }) {
		switch n, _ := strconv.Atoi(p); {
		case n == 2:
			return "faint text"
		case n >= 30 && n <= 37, n >= 40 && n <= 47, n >= 90 && n <= 97, n >= 100 && n <= 107:
			return "an ANSI color"
		case n == 38 || n == 48 || n == 58:
			return "an extended color"
		}
	}
	return ""
}

// TestColorlessThemesDrawNoColor draws the chats, a chat with a selected
// message, the help and the command line with the plain theme, which
// NO_COLOR selects, and with the high-contrast one, and checks that no
// frame sets a color or faint text: nothing in the UI colors itself
// outside the theme.
func TestColorlessThemesDrawNoColor(t *testing.T) {
	for _, th := range []*theme.Theme{theme.Plain(), theme.HighContrast()} {
		m := goldenModelWith(t, 120, 40, th)
		frames := []string{m.View().Content}
		_, open := m.Update(key("enter"))
		m.Update(open())
		for _, k := range []string{"tab", "k", "k"} {
			m.Update(key(k))
		}
		frames = append(frames, m.View().Content)
		m.Update(key("?"))
		frames = append(frames, m.View().Content)
		m.Update(key("esc"))
		m.Update(key(":"))
		m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
		frames = append(frames, m.View().Content)
		for i, f := range frames {
			for _, seq := range sgrSequence.FindAllStringSubmatch(f, -1) {
				if what := colorOrFaint(seq[1]); what != "" {
					t.Fatalf("%s, frame %d: %q sets %s", th.ID, i, seq[0], what)
				}
			}
		}
		if !strings.Contains(frames[1], "\x1b[") {
			t.Fatalf("%s: no styling at all; the test sees nothing", th.ID)
		}
	}
}
