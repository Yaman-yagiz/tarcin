// SPDX-License-Identifier: GPL-3.0-or-later

// Package textfold builds search keys that match text regardless of case,
// diacritics and the Turkish dotted/dotless i. "kadin" finds "Kadın" and
// "KADIN"; "istanbul" finds "İSTANBUL"; "cagri" finds "Çağrı".
//
// The folding is locale-independent on purpose: all four Turkish i forms
// (I, ı, İ, i) become "i", so the result does not depend on the user's
// locale or keyboard layout. The same key is stored in the SQLite FTS5
// index (docs/architecture/data-model.md §3), so in-memory and database
// searches agree.
package textfold

import (
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// revision is bumped whenever Fold's rules change.
const revision = 1

// Version identifies Fold's output: its rules and the Unicode tables of
// golang.org/x/text and of Go that it uses. Search keys stored under one
// version must be rebuilt when it changes, because the same text may
// fold differently.
func Version() string {
	return fmt.Sprintf("%d/x-text-%s/go-%s", revision, norm.Version, unicode.Version)
}

// casers are reused because a cases.Caser must not be shared between
// goroutines.
var casers = sync.Pool{New: func() any { c := cases.Fold(); return &c }}

// Fold returns the search key of s: case-folded, with combining marks and
// invisible format characters removed and every Turkish i form mapped to
// "i". Fold is idempotent.
func Fold(s string) string {
	if isASCII(s) {
		return strings.ToLower(s)
	}
	// Normalization skips text next to invalid bytes, so repair the
	// encoding first; otherwise a second pass would decompose more.
	s = strings.ToValidUTF8(s, "\ufffd")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
			// Diacritics (after decomposition) and invisible characters.
		case r == 'ı':
			b.WriteByte('i')
		default:
			b.WriteRune(r)
		}
	}
	c := casers.Get().(*cases.Caser)
	defer casers.Put(c)
	out := c.String(b.String())
	if strings.IndexFunc(out, isCherokee) >= 0 {
		out = strings.Map(cherokeeUpper, out)
	}
	// Full case folding of a few characters yields sequences containing
	// combining marks; folding once more keeps Fold mark-free and
	// idempotent in that rare case.
	if strings.ContainsRune(out, 'ı') || hasMarks(out) {
		return Fold(out)
	}
	return out
}

// Unicode case folding maps lowercase Cherokee to uppercase and leaves
// uppercase Cherokee alone (CaseFolding.txt), but x/text v0.42 folds the
// uppercase letters back to lowercase, so its output is not idempotent.
// Pin Cherokee to the uppercase form the standard specifies.
func isCherokee(r rune) bool {
	return r >= 0x13a0 && r <= 0x13fd || r >= 0xab70 && r <= 0xabbf
}

func cherokeeUpper(r rune) rune {
	switch {
	case r >= 0xab70 && r <= 0xabbf: // small letters A..MV -> 13A0..13EF
		return r - 0xab70 + 0x13a0
	case r >= 0x13f8 && r <= 0x13fd: // small letters YE..MV -> 13F0..13F5
		return r - 8
	}
	return r
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func hasMarks(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
			return true
		}
	}
	return false
}

// Query is a parsed search query: every whitespace-separated term must
// occur in the searched text. The zero Query matches everything.
type Query struct {
	terms []string
}

// NewQuery folds and splits q.
func NewQuery(q string) Query {
	return Query{terms: strings.Fields(Fold(q))}
}

// Empty reports whether the query has no terms.
func (q Query) Empty() bool { return len(q.terms) == 0 }

// Match reports whether s contains every term of the query.
func (q Query) Match(s string) bool {
	if q.Empty() {
		return true
	}
	return q.MatchFolded(Fold(s))
}

// MatchFolded is Match for text already passed through Fold. Callers that
// search the same strings repeatedly should cache their folded form.
func (q Query) MatchFolded(folded string) bool {
	for _, t := range q.terms {
		if !strings.Contains(folded, t) {
			return false
		}
	}
	return true
}
