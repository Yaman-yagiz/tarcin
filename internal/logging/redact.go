// SPDX-License-Identifier: GPL-3.0-or-later

package logging

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// maxText is the longest text a log line keeps; the rest is cut. It caps
// what a dump in a library's message can carry.
const maxText = 2048

var (
	// jidPattern matches WhatsApp addresses: a user, group or LID part,
	// optionally with agent and device, and a WhatsApp server.
	jidPattern = regexp.MustCompile(`[\w.+:-]+@(?:s\.whatsapp\.net|c\.us|lid|g\.us|broadcast|newsletter|hosted\.lid|hosted|bot|call|msgr|interop)\b`)
	// digitsPattern matches runs of eight or more digits: phone numbers,
	// and the user parts of addresses written without a server.
	digitsPattern = regexp.MustCompile(`\+?\b\d{8,}\b`)
	// tagPattern matches an XML start or empty tag with attributes, as in
	// a protocol node dump; the attributes go.
	tagPattern = regexp.MustCompile(`<([A-Za-z_][\w:.-]*)\s[^<>]*?(/?)>`)
	// textPattern matches text between two tags of such a dump.
	textPattern = regexp.MustCompile(`>([^<>]+)<`)
)

// Redactor replaces what identifies people in log text with short keyed
// hashes: the same address gives the same hash within one run, so lines
// can be followed, but the hash cannot be reversed by trying every phone
// number, because the key is random and never stored.
type Redactor struct {
	key []byte
}

// NewRedactor returns a redactor with a fresh random key.
func NewRedactor() *Redactor {
	key := make([]byte, 32)
	_, _ = rand.Read(key) // crypto/rand.Read never fails
	return &Redactor{key: key}
}

// String redacts s: WhatsApp addresses become jid#hash and long digit
// runs num#hash; tags of a protocol node dump lose their attributes and
// text. The result is cut to maxText bytes.
func (r *Redactor) String(s string) string {
	s = jidPattern.ReplaceAllStringFunc(s, func(jid string) string { return "jid#" + r.hash(bare(jid)) })
	s = digitsPattern.ReplaceAllStringFunc(s, func(n string) string { return "num#" + r.hash(strings.TrimPrefix(n, "+")) })
	if strings.ContainsRune(s, '<') {
		s = tagPattern.ReplaceAllString(s, "<$1 …$2>")
		s = textPattern.ReplaceAllString(s, ">…<")
	}
	if len(s) > maxText {
		s = strings.ToValidUTF8(s[:maxText], "") + "…"
	}
	return s
}

// bare drops the agent and device of an address, so that every device
// of a person hashes the same.
func bare(jid string) string {
	user, server, _ := strings.Cut(jid, "@")
	if i := strings.IndexAny(user, ":_"); i >= 0 {
		user = user[:i]
	}
	return user + "@" + server
}

func (r *Redactor) hash(s string) string {
	m := hmac.New(sha256.New, r.key)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil)[:3])
}
