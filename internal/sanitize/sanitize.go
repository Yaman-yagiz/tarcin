// SPDX-License-Identifier: GPL-3.0-or-later

// Package sanitize makes untrusted text safe to print to a terminal.
//
// Every string that originates from the network (message bodies, display
// names, group subjects, file names) must pass through Text or Line
// before it reaches the screen (docs/architecture/ui.md §7). The output
// never contains escape sequences, control characters other than
// newline, bidi embedding/override/isolate controls, or invalid UTF-8.
// Both functions are idempotent.
package sanitize

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// TabWidth is the number of spaces a tab expands to in Text.
	TabWidth = 4
	// maxMarksText and maxMarksLine cap consecutive combining marks
	// ("zalgo" text), which can overflow lines and slow width checks.
	maxMarksText = 8
	maxMarksLine = 3
)

// Text sanitizes a message body. Newlines are kept; tabs become spaces;
// escape sequences and control characters are removed; bidi embedding,
// override and isolate controls are replaced by visible markers such as
// "[RLO]" so they cannot reorder the text around them.
func Text(s string) string {
	if safeASCII(s, false) {
		return s
	}
	return clean(s, false)
}

// Line sanitizes text that must fit on one line: display names, group
// subjects and file names. In addition to what Text does, line breaks
// and other whitespace collapse to single spaces, the result is trimmed,
// and bidi controls and invisible characters are removed entirely, so a
// name can neither spoof another nor appear blank.
func Line(s string) string {
	if safeASCII(s, true) {
		return s
	}
	return clean(s, true)
}

// safeASCII reports whether s is printable ASCII that clean would return
// unchanged, so the common case allocates nothing.
func safeASCII(s string, line bool) bool {
	prevSpace := true // treat the start as a space to reject leading spaces
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			if line && prevSpace {
				return false
			}
			prevSpace = true
			continue
		case c == '\n':
			if line {
				return false
			}
		case c < 0x20 || c >= 0x7f:
			return false
		}
		prevSpace = false
	}
	return !line || !prevSpace || s == ""
}

func clean(s string, line bool) string {
	var b strings.Builder
	b.Grow(len(s))
	maxMarks := maxMarksText
	if line {
		maxMarks = maxMarksLine
	}
	// marks counts consecutive combining marks since the last written base
	// character. Only output resets it: dropped input (controls, escape
	// sequences, invisible characters) must not, or removing it would let
	// two runs of marks merge and exceed the limit on a second pass.
	marks := 0
	pendingSpace := false // Line: a space is owed before the next visible rune

	writeBase := func(r rune) {
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
		marks = 0
	}
	writeRaw := func(str string) {
		b.WriteString(str)
		marks = 0
	}
	space := func() {
		if b.Len() > 0 {
			pendingSpace = true
		}
	}

	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			i++
			switch {
			case c == 0x1b:
				i = skipEscape(s, i)
			case c == '\n':
				if line {
					space()
				} else {
					writeRaw("\n")
				}
			case c == '\t':
				if line {
					space()
				} else {
					writeRaw(strings.Repeat(" ", TabWidth))
				}
			case c == ' ':
				if line {
					space()
				} else {
					writeRaw(" ")
				}
			case c < 0x20 || c == 0x7f:
				// Other C0 controls and DEL are dropped.
			default:
				writeBase(rune(c))
			}
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			writeBase(utf8.RuneError)
		case line && isInvisible(r):
			// Dropped from names, before the marks: some invisible
			// characters (the Khmer inherent vowels) are marks.
		case unicode.In(r, unicode.Mn, unicode.Me):
			if pendingSpace { // the owed space becomes the marks' base
				b.WriteByte(' ')
				pendingSpace = false
				marks = 0
			}
			if marks < maxMarks {
				b.WriteRune(r)
			}
			marks++
		case r >= 0x80 && r <= 0x9f:
			i = skipC1(s, i, r)
		case bidiControlName(r) != "":
			if !line {
				writeRaw("[" + bidiControlName(r) + "]")
			}
		case r == ' ' || r == ' ':
			if line {
				space()
			} else {
				writeRaw("\n")
			}
		case line && isBidiMark(r):
			// Dropped from names; harmless and sometimes needed in bodies.
		case line && unicode.IsSpace(r):
			space()
		default:
			writeBase(r)
		}
	}
	return b.String()
}

// bidiControlName returns the abbreviation of a bidi embedding, override
// or isolate control, or "" for any other rune.
func bidiControlName(r rune) string {
	switch r {
	case '\u202a':
		return "LRE"
	case '\u202b':
		return "RLE"
	case '\u202c':
		return "PDF"
	case '\u202d':
		return "LRO"
	case '\u202e':
		return "RLO"
	case '\u2066':
		return "LRI"
	case '\u2067':
		return "RLI"
	case '\u2068':
		return "FSI"
	case '\u2069':
		return "PDI"
	}
	return ""
}

// isBidiMark reports whether r is a directional mark. Marks cannot
// reorder text; they are kept in bodies, where RTL text uses them.
func isBidiMark(r rune) bool {
	return r == '\u200e' || r == '\u200f' || r == '\u061c'
}

// isInvisible reports whether r renders as nothing. Such characters are
// removed from names so a name cannot look blank or impersonate another;
// zero-width joiners and variation selectors stay, as emoji need them.
func isInvisible(r rune) bool {
	switch r {
	case '\u00ad', // soft hyphen
		'ᅟ', 'ᅠ', 'ㅤ', 'ﾠ', // Hangul fillers
		'\u17b4', '\u17b5', // Khmer inherent vowels
		'\u180e', // Mongolian vowel separator
		'\u200b', // zero-width space
		'\ufeff': // zero-width no-break space (BOM)
		return true
	}
	return r >= '\u2060' && r <= '\u2064' || // word joiner, invisible operators
		r >= '\u206a' && r <= '\u206f' || // deprecated format characters
		r >= '\ufff9' && r <= '\ufffb' // interlinear annotation
}
