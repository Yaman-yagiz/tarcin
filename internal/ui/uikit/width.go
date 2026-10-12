// SPDX-License-Identifier: GPL-3.0-or-later

package uikit

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Display prepares sanitized text for the screen under the given width
// method. With grapheme widths (the terminal reported Unicode core mode
// 2027) text is shown as is. With wcwidth, emoji sequences are a known
// source of disagreement: naive wcwidth counts "❤\ufe0f" as one cell and a
// ZWJ family as six, while many terminals draw both as two, and terminals
// draw a flag (a pair of regional indicators) as two, four or three
// cells. A line that the terminal finds wider than the renderer does
// wraps and shifts the whole frame. Display therefore removes the code
// points the models disagree on (variation selectors, the zero-width
// joiner, skin-tone modifiers and tag characters) and spells flags as
// their letters, "[TR]". What remains is drawn the same by every
// terminal. Only the display changes; message content is untouched.
func Display(method ansi.Method, s string) string {
	if method == ansi.GraphemeWidth || !strings.ContainsFunc(s, ambiguous) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	var flag []rune // pending regional indicators
	flush := func() {
		for len(flag) > 0 {
			n := min(len(flag), 2)
			b.WriteByte('[')
			for _, r := range flag[:n] {
				b.WriteRune('A' + (r - regionalA))
			}
			b.WriteByte(']')
			flag = flag[n:]
		}
	}
	for _, r := range s {
		switch {
		case isRegional(r):
			flag = append(flag, r)
		case ambiguousEmoji(r):
			// dropped
		default:
			flush()
			b.WriteRune(r)
		}
	}
	flush()
	return b.String()
}

const regionalA = 0x1f1e6 // REGIONAL INDICATOR SYMBOL LETTER A

func isRegional(r rune) bool { return r >= regionalA && r <= regionalA+25 }

func ambiguous(r rune) bool { return isRegional(r) || ambiguousEmoji(r) }

// ambiguousEmoji reports whether r changes the width of the emoji before
// it under some width models but not others.
func ambiguousEmoji(r rune) bool {
	switch {
	case r == '\u200d': // zero-width joiner
		return true
	case r == '\ufe0e' || r == '\ufe0f': // text and emoji presentation selectors
		return true
	case r >= 0x1f3fb && r <= 0x1f3ff: // skin-tone modifiers
		return true
	case r >= 0xe0020 && r <= 0xe007f: // tag characters (subdivision flags)
		return true
	}
	return false
}

// Truncate cuts s to at most w cells under method, ending it with tail
// if it is cut. ansi's Truncate can overshoot under wcwidth: it keeps a
// grapheme cluster whole by its first rune's width, while wcwidth also
// counts a spacing mark after it ("\ufffd" and U+1031 are two cells), so
// a cut line could still be too wide and shift the frame. Such a line is
// cut shorter until it fits.
func Truncate(method ansi.Method, s string, w int, tail string) string {
	out := method.Truncate(s, w, tail)
	for n := w - 1; n >= 0 && method.StringWidth(out) > w; n-- {
		out = method.Truncate(s, n, tail)
	}
	return out
}
