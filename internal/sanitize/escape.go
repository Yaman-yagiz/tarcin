// SPDX-License-Identifier: GPL-3.0-or-later

package sanitize

// The parsers below follow ECMA-48. Each takes the index just past the
// sequence introducer and returns the index just past the whole sequence,
// so the sequence is dropped rather than only its first byte. A sequence
// cut short by an unexpected byte ends there, and parsing resumes at that
// byte, as a terminal would.

// C1 controls as runes (encoded as two bytes in UTF-8).
const (
	c1DCS = 0x90
	c1SOS = 0x98
	c1CSI = 0x9b
	c1ST  = 0x9c
	c1OSC = 0x9d
	c1PM  = 0x9e
	c1APC = 0x9f
)

// skipEscape skips an escape sequence whose ESC byte precedes index i.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	c := s[i]
	switch {
	case c == '[':
		return skipCSI(s, i+1)
	case c == ']':
		return skipString(s, i+1, true)
	case c == 'P' || c == 'X' || c == '^' || c == '_': // DCS, SOS, PM, APC
		return skipString(s, i+1, false)
	case c >= 0x20 && c <= 0x2f: // intermediate bytes, then a final byte
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
			i++
		}
		return i
	case c >= 0x30 && c <= 0x7e: // two-byte sequence such as ESC c or ESC 7
		return i + 1
	}
	return i // a lone ESC: drop it and keep the next byte
}

// skipC1 skips a C1 control (already decoded as r, ending before i) and
// the control string or sequence it introduces.
func skipC1(s string, i int, r rune) int {
	switch r {
	case c1CSI:
		return skipCSI(s, i)
	case c1OSC:
		return skipString(s, i, true)
	case c1DCS, c1SOS, c1PM, c1APC:
		return skipString(s, i, false)
	}
	return i
}

// skipCSI skips control-sequence parameter (0x30–0x3f) and intermediate
// (0x20–0x2f) bytes and the final byte (0x40–0x7e).
func skipCSI(s string, i int) int {
	for i < len(s) {
		c := s[i]
		switch {
		case c >= 0x40 && c <= 0x7e:
			return i + 1
		case c >= 0x20 && c <= 0x3f:
			i++
		default:
			return i
		}
	}
	return i
}

// skipString skips a control string up to and including its terminator:
// ST (ESC \ or C1 ST) and, for OSC, also BEL. An unterminated string runs
// to the end of the text; hiding it is safe, printing it is not.
func skipString(s string, i int, belEnds bool) int {
	for i < len(s) {
		switch c := s[i]; {
		case c == 0x07 && belEnds:
			return i + 1
		case c == 0x1b:
			if i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			return i // another sequence starts here
		case c == 0xc2 && i+1 < len(s) && s[i+1] == c1ST:
			return i + 2
		}
		i++
	}
	return i
}
