// SPDX-License-Identifier: GPL-3.0-or-later

package sanitize

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Yaman-yagiz/tarcin/internal/devtools/synth"
)

func TestText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello", "hello"},
		{"Çağrı ışık İstanbul 👍", "Çağrı ışık İstanbul 👍"},
		{"\x1b]0;pwned\x07title change", "title change"},
		{"\x1b]52;c;cm0gLXJmIH4K\x07clipboard write", "clipboard write"},
		{"\x1b[2J\x1b[Hscreen cleared", "screen cleared"},
		{"\x1b]8;;https://evil.example\x1b\\click me\x1b]8;;\x1b\\", "click me"},
		{"\x1b[?1049lleave", "leave"},
		{"\x1bPq#0;2;0;0;0#0~~@@vv\x1b\\after", "after"},
		{"\x1b_Gf=24,s=1,v=1;AAAA\x1b\\after", "after"},
		{"\x1b^privacy message\x1b\\after", "after"},
		{"\x1bXstart of string\x1b\\after", "after"},
		{"\u009b31mred", "red"},
		{"\u009d0;title\u009cafter", "after"},
		{"\u0090dcs\u009cafter", "after"},
		{"\u0085next line", "next line"},
		{"\x1b7saved\x1b8", "saved"},
		{"\x1b(Bascii", "ascii"},
		{"\x1b", ""},
		{"\x1b[", ""},
		{"\x1b[31", ""},
		{"\x1b]0;unterminated title", ""},
		{"\x1b\x01x", "x"},
		{"\x1b[31\nX", "\nX"},
		{"\x1b]0;a\x1b[31mb", "b"},
		{"\x00NUL\x07BEL\x08BS\x7fDEL\rCR", "NULBELBSDELCR"},
		{"tab\there", "tab    here"},
		{"multi\nline\n", "multi\nline\n"},
		{"line1\u2028line2\u2029line3", "line1\nline2\nline3"},
		{"invoice\u202efdp.exe", "invoice[RLO]fdp.exe"},
		{"\u2066a\u2069 \u202ab\u202c \u202dc \u202bd \u2067e \u2068f", "[LRI]a[PDI] [LRE]b[PDF] [LRO]c [RLE]d [RLI]e [FSI]f"},
		{"rtl \u200fmark kept", "rtl \u200fmark kept"},
		{"zero\u200bwidth", "zero\u200bwidth"},
		{"👨\u200d👩\u200d👧", "👨\u200d👩\u200d👧"},
		{"bad \xff\xfe end", "bad �� end"},
		{"E\u0301lif", "E\u0301lif"},
		{"a" + strings.Repeat("\u0301", 20), "a" + strings.Repeat("\u0301", maxMarksText)},
	}
	for _, tt := range tests {
		if got := Text(tt.in); got != tt.want {
			t.Errorf("Text(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLine(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Ayşe Yılmaz", "Ayşe Yılmaz"},
		{"  Ayşe \n Yılmaz\t ", "Ayşe Yılmaz"},
		{"a  b", "a b"},
		{" lead", "lead"},
		{"trail ", "trail"},
		{"\u202eevil name", "evil name"},
		{"\u2066iso\u2069late", "isolate"},
		{"r\u200ftl\u200e mark", "rtl mark"},
		{"A\u200bli", "Ali"},
		{"soft\u00adhyphen", "softhyphen"},
		{"ㅤㅤ", ""},
		{"ᅟᅠ", ""},
		{"\ufeffbom", "bom"},
		{"word\u2060joiner", "wordjoiner"},
		{"Zeynep 👩\u200d💻", "Zeynep 👩\u200d💻"},
		{"🏳\ufe0f\u200d🌈", "🏳\ufe0f\u200d🌈"},
		{"x\x1b[31my", "xy"},
		{"a\u2028b", "a b"},
		{" nbsp em ", "nbsp em"},
		{"E" + strings.Repeat("\u0301", 5), "E" + strings.Repeat("\u0301", maxMarksLine)},
		{"report.pdf\x00.exe", "report.pdf.exe"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		if got := Line(tt.in); got != tt.want {
			t.Errorf("Line(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// checkText reports why out is not a safe Text result, or "".
func checkText(out string) string {
	if !utf8.ValidString(out) {
		return "invalid UTF-8"
	}
	for _, r := range out {
		switch {
		case r == '\n':
		case r < 0x20 || r == 0x7f:
			return fmt.Sprintf("C0 control U+%04X", r)
		case r >= 0x80 && r <= 0x9f:
			return fmt.Sprintf("C1 control U+%04X", r)
		case bidiControlName(r) != "":
			return fmt.Sprintf("bidi control U+%04X", r)
		case r == '\u2028' || r == '\u2029':
			return "line or paragraph separator"
		}
	}
	return ""
}

// checkLine reports why out is not a safe Line result, or "".
func checkLine(out string) string {
	if msg := checkText(out); msg != "" {
		return msg
	}
	switch {
	case strings.Contains(out, "\n"):
		return "newline"
	case strings.HasPrefix(out, " ") || strings.HasSuffix(out, " "):
		return "untrimmed"
	case strings.Contains(out, "  "):
		return "double space"
	}
	for _, r := range out {
		if isInvisible(r) || isBidiMark(r) {
			return fmt.Sprintf("invisible U+%04X", r)
		}
	}
	return ""
}

func TestHostileCorpus(t *testing.T) {
	for i, in := range synth.Hostile() {
		text, line := Text(in), Line(in)
		if msg := checkText(text); msg != "" {
			t.Errorf("hostile[%d]: Text output has %s", i, msg)
		}
		if msg := checkLine(line); msg != "" {
			t.Errorf("hostile[%d]: Line output has %s", i, msg)
		}
		if Text(text) != text || Line(line) != line {
			t.Errorf("hostile[%d]: not idempotent", i)
		}
	}
}

func TestFastPathDoesNotAllocate(t *testing.T) {
	s := strings.Repeat("plain ascii message text ", 20)
	if n := testing.AllocsPerRun(100, func() { _ = Text(s) }); n != 0 {
		t.Errorf("Text allocated %v times on plain ASCII", n)
	}
	name := "Plain Name"
	if n := testing.AllocsPerRun(100, func() { _ = Line(name) }); n != 0 {
		t.Errorf("Line allocated %v times on plain ASCII", n)
	}
}

func fuzzSeeds(f *testing.F) {
	for _, s := range synth.Hostile() {
		if len(s) < 1<<12 {
			f.Add(s)
		}
	}
	for _, s := range []string{"", " ", "a\x1b", "\x1b[1;2", "\xc2\x9c", "\u202e\u2066", "e\u0301\u0301", "\t\n\r"} {
		f.Add(s)
	}
}

func FuzzText(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, in string) {
		out := Text(in)
		if msg := checkText(out); msg != "" {
			t.Fatalf("Text(%q) = %q has %s", in, out, msg)
		}
		if again := Text(out); again != out {
			t.Fatalf("Text is not idempotent: %q -> %q -> %q", in, out, again)
		}
	})
}

func FuzzLine(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, in string) {
		out := Line(in)
		if msg := checkLine(out); msg != "" {
			t.Fatalf("Line(%q) = %q has %s", in, out, msg)
		}
		if again := Line(out); again != out {
			t.Fatalf("Line is not idempotent: %q -> %q -> %q", in, out, again)
		}
	})
}

func BenchmarkText(b *testing.B) {
	inputs := map[string]string{
		"ascii":   strings.Repeat("plain ascii message text ", 8),
		"turkish": strings.Repeat("Çağrı akşam yemeğine geliyor mu? 👍 ", 6),
		"hostile": strings.Join(synth.Hostile()[:20], " "),
		"1MB":     synth.Hostile()[len(synth.Hostile())-1],
	}
	for name, in := range inputs {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(in)))
			for b.Loop() {
				_ = Text(in)
			}
		})
	}
}

// TestMarksCapSurvivesJoiners: joiners between runs of combining marks
// must not reset the cap, or one cell could carry thousands of marks.
func TestMarksCapSurvivesJoiners(t *testing.T) {
	zalgo := "a" + strings.Repeat(strings.Repeat("\u0301", 8)+"\u200d", 500)
	for name, got := range map[string]string{"Text": Text(zalgo), "Line": Line(zalgo)} {
		marks := 0
		for _, r := range got {
			if unicode.Is(unicode.Mn, r) {
				marks++
			}
		}
		if limit := map[string]int{"Text": maxMarksText, "Line": maxMarksLine}[name]; marks > limit {
			t.Errorf("%s kept %d marks on one base, want at most %d", name, marks, limit)
		}
	}
	// Emoji sequences keep their joiners.
	if family := "\U0001F468\u200d\U0001F469\u200d\U0001F467"; Text(family) != family {
		t.Fatalf("Text(family) = %q", Text(family))
	}
}
