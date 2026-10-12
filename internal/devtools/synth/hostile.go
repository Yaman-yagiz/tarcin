// SPDX-License-Identifier: GPL-3.0-or-later

package synth

import "strings"

// Hostile returns a corpus of message bodies that attack or stress a
// terminal renderer: escape and control sequences, bidi overrides,
// invisible characters, invalid UTF-8 and pathological sizes (scenario
// S12 in docs/architecture/performance.md). Every call returns a fresh
// slice with the same contents.
func Hostile() []string {
	return []string{
		"\x1b]0;pwned\x07title change",                             // OSC 0: window title
		"\x1b]52;c;cm0gLXJmIH4K\x07clipboard write",                // OSC 52: clipboard
		"\x1b[2J\x1b[Hscreen cleared",                              // CSI: clear screen, home
		"\x1b]8;;https://evil.example\x1b\\click me\x1b]8;;\x1b\\", // OSC 8 hyperlink
		"\x1b[?1049lleave alternate screen",                        // DEC private mode
		"\x1bPq#0;2;0;0;0#0~~@@vv\x1b\\",                           // DCS: sixel
		"\x1b_Gf=24,s=1,v=1;AAAA\x1b\\",                            // APC: kitty graphics
		"\u009b31mC1 CSI in red",                                   // C1 control (8-bit CSI)
		"\x00NUL\x07BEL\x08BS\x7fDEL\rCR overwrite",                // C0 controls
		"invoice\u202efdp.exe",                                     // RLO: renders as "invoiceexe.pdf"
		"\u2066isolate\u2069 \u202aembed\u202c \u200fmark",         // bidi isolates, embeddings, marks
		"zero\u200bwidth\u200cjoin\u200dno\u2060break\ufeffbom",    // invisible characters
		"a" + strings.Repeat("\u0301", 200),                        // combining-mark flood ("zalgo")
		"bad utf-8: \xff\xfe\xc3\x28 end",                          // invalid UTF-8
		strings.Repeat("unbroken", 1250),                           // 10,000 characters without spaces
		"مرحبا كيف حالك hello שלום עולם mixed",                     // RTL scripts mixed with LTR
		"👨\u200d👩\u200d👧\u200d👦🏳\ufe0f\u200d🌈🇹🇷👍🏽 emoji sequences", // ZWJ sequences, flags, modifiers
		"田中太郎 全角文字 ＡＢＣ",                                            // double-width characters
		"line1\nline2\n\n\n\n\n\n\n\n\nline10",                     // many line breaks
		"tab\tseparated\tcolumns\t\t\tend",                         // tabs
		strings.Repeat("1 MB of text. ", 1<<20/14),                 // about 1 MB
	}
}

// StressName returns a display name that stresses width computation and
// truncation: very long, emoji, right-to-left, double-width, invisible or
// combining characters (scenario S4).
func (g *Gen) StressName() string {
	names := []string{
		strings.Repeat("Çok Uzun İsimli Kullanıcı ", 6),
		"محمد عبد الله",
		"דוד כהן",
		"田中 太郎",
		"Zeynep 👩\u200d💻🚀✨",
		"A\u200bli\u200d",
		"E\u0301lif Yi\u0307lmaz",
		"\u202eevil name",
		"🇹🇷🇹🇷🇹🇷",
		"x",
	}
	if g.r.IntN(2) == 0 {
		return g.Name()
	}
	return pick(g, names)
}
