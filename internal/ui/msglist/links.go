// SPDX-License-Identifier: GPL-3.0-or-later

package msglist

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Web addresses in message text are marked as hyperlinks (OSC 8) where
// the message list draws them. A terminal that guesses links from the
// screen sees only the part of a long address before the line breaks
// it; a hyperlink carries the whole address on every part. Only http and
// https addresses are linked, and a link's target is always the address
// its text shows.

// urlPattern finds web addresses: http(s) URLs and "www." hosts, up to
// the next space or quote.
var urlPattern = regexp.MustCompile("(?i)\\b(?:https?://|www\\.)[^\\s<>\"'`]+")

// maxLinkLen bounds a link's target.
const maxLinkLen = 2048

// link is a web address in a text: its byte span and its target.
type link struct {
	start, end int
	target     string
}

// findLinks returns the web addresses in plain text, without the
// punctuation that usually follows an address in a sentence, and only
// those with a safe target.
func findLinks(text string) []link {
	var out []link
	for _, loc := range urlPattern.FindAllStringIndex(text, -1) {
		s := trimLink(text[loc[0]:loc[1]])
		if target, ok := linkTarget(s); ok {
			out = append(out, link{start: loc[0], end: loc[0] + len(s), target: target})
		}
	}
	return out
}

// Links returns the targets of the web addresses in text, in order, for
// opening a message's link.
func Links(text string) []string {
	var out []string
	for _, l := range findLinks(text) {
		out = append(out, l.target)
	}
	return out
}

// trimLink drops what follows an address in a sentence: final
// punctuation, a quote, the markers of WhatsApp's bold and italic, and
// a closing bracket the address did not open.
func trimLink(s string) string {
	for s != "" {
		r, size := utf8.DecodeLastRuneInString(s)
		switch {
		case strings.ContainsRune(".,;:!?…*_~", r):
		case r == ')' && strings.Count(s, "(") < strings.Count(s, ")"):
		case r == ']' && strings.Count(s, "[") < strings.Count(s, "]"):
		case r == '}' && strings.Count(s, "{") < strings.Count(s, "}"):
		default:
			return s
		}
		s = s[:len(s)-size]
	}
	return s
}

// linkTarget returns the address a link opens: an http or https URL with
// a host and no user name, written in printable ASCII (the path's other
// characters escaped), or false.
func linkTarget(s string) (string, bool) {
	if strings.HasPrefix(strings.ToLower(s), "www.") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	for i := 0; i < len(u.Host); i++ {
		if u.Host[i] >= utf8.RuneSelf {
			return "", false // a host in another script: left to the terminal, which shows it
		}
	}
	target := u.String()
	if len(target) > maxLinkLen {
		return "", false
	}
	for i := 0; i < len(target); i++ {
		if c := target[i]; c <= ' ' || c >= 0x7f {
			return "", false
		}
	}
	return target, true
}

// linkify marks the web addresses of a paragraph as hyperlinks in its
// wrapped lines, part by part. It follows the paragraph's text through
// the lines, which the wrap only split and stripped of spaces at the
// breaks; where it cannot (a character cut off), the lines from there
// on stay as they are. id names the paragraph, so that a terminal shows
// the parts of one link as one.
func linkify(para string, lines []string, id string) []string {
	plain := ansi.Strip(para)
	links := findLinks(plain)
	if len(links) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	pos := 0 // the next byte of plain to find in the lines
	for i, line := range lines {
		var b strings.Builder
		open := -1
		for j := 0; j < len(line); {
			if line[j] == '\x1b' {
				n := escapeLen(line[j:])
				b.WriteString(line[j : j+n])
				j += n
				continue
			}
			r, size := utf8.DecodeRuneInString(line[j:])
			for pos < len(plain) && plain[pos] == ' ' && r != ' ' {
				pos++ // a space the wrap dropped at a break
			}
			if !strings.HasPrefix(plain[pos:], line[j:j+size]) {
				copy(out[i:], lines[i:])
				return out
			}
			if in := linkAt(links, pos); in != open {
				if open >= 0 {
					b.WriteString(ansi.ResetHyperlink())
				}
				if in >= 0 {
					b.WriteString(ansi.SetHyperlink(links[in].target, "id="+id+"-"+strconv.Itoa(in)))
				}
				open = in
			}
			b.WriteString(line[j : j+size])
			j += size
			pos += size
		}
		if open >= 0 {
			b.WriteString(ansi.ResetHyperlink())
		}
		out[i] = b.String()
	}
	return out
}

// linkAt returns the index of the link that covers byte pos, or -1.
func linkAt(links []link, pos int) int {
	for i, l := range links {
		if pos >= l.start && pos < l.end {
			return i
		}
	}
	return -1
}

// escapeLen returns the length of the escape sequence s starts with: a
// CSI (styles), an OSC up to its terminator, or a two-byte sequence.
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
	case ']':
		for i := 2; i < len(s); i++ {
			if s[i] == '\a' {
				return i + 1
			}
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
	default:
		return 2
	}
	return len(s)
}
