// SPDX-License-Identifier: GPL-3.0-or-later

package uikit

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

var emojiSamples = []string{
	"❤\ufe0f", // red heart with emoji presentation
	"\U0001f468\u200d\U0001f469\u200d\U0001f467\u200d\U0001f466", // family (ZWJ)
	"\U0001f44d\U0001f3fd", // thumbs up, medium skin tone
	"\U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f", // Scotland (tags)
	"\U0001f3f3\ufe0f\u200d\U0001f308",                                       // rainbow flag
	"1\ufe0f\u20e3",                                                          // keycap one
	"\U0001f1f9\U0001f1f7",                                                   // flag of Turkey (regional indicators)
	"☺\ufe0e",                                                                // smiling face, text presentation
	"plain Türkçe text \U0001f600",
}

// TestDisplayMakesWidthModelsAgree is the property Display exists for:
// after it, naive wcwidth and grapheme widths measure the same.
func TestDisplayMakesWidthModelsAgree(t *testing.T) {
	for _, s := range emojiSamples {
		out := Display(ansi.WcWidth, s)
		wc, gr := ansi.WcWidth.StringWidth(out), ansi.GraphemeWidth.StringWidth(out)
		if wc != gr {
			t.Errorf("Display(%q) = %q: wcwidth %d, grapheme %d", s, out, wc, gr)
		}
	}
}

func TestDisplay(t *testing.T) {
	if got := Display(ansi.WcWidth, "❤\ufe0f ok"); got != "❤ ok" {
		t.Errorf("got %q", got)
	}
	s := "\U0001f468\u200d\U0001f469\u200d\U0001f467"
	if got := Display(ansi.GraphemeWidth, s); got != s {
		t.Error("grapheme mode must keep sequences intact")
	}
	if got := Display(ansi.WcWidth, "Bravo \U0001f1f9\U0001f1f7\U0001f1e9\U0001f1ea \U0001f1fa!"); got != "Bravo [TR][DE] [U]!" {
		t.Errorf("flags: got %q", got)
	}
	plain := "no emoji here"
	if n := testing.AllocsPerRun(100, func() { _ = Display(ansi.WcWidth, plain) }); n != 0 {
		t.Errorf("plain text allocated %v times", n)
	}
}

func TestTruncateNeverOvershoots(t *testing.T) {
	// U+FFFD and the Myanmar vowel sign U+1031 are one cluster, which
	// wcwidth counts as two cells and ansi's Truncate keeps as one.
	s := "   \ufffd\u1031xyz"
	for _, m := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		for w := 0; w <= 8; w++ {
			if got := Truncate(m, s, w, ""); m.StringWidth(got) > w {
				t.Errorf("method %v: Truncate(%q, %d) = %q, %d cells", m, s, w, got, m.StringWidth(got))
			}
			if got := Fit(m, s, w); m.StringWidth(got) != w {
				t.Errorf("method %v: Fit(%q, %d) = %q, %d cells", m, s, w, got, m.StringWidth(got))
			}
		}
	}
}
