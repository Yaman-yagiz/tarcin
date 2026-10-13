// SPDX-License-Identifier: GPL-3.0-or-later

package msglist

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/devtools/synth"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// sgr matches the styling the themes write, and hyperlinkSeq the
// hyperlinks the list writes around web addresses; any other escape
// sequence on screen came from content.
var (
	sgr          = regexp.MustCompile("\x1b\\[[0-9;:]*m")
	hyperlinkSeq = regexp.MustCompile("\x1b\\]8;(?:id=[0-9a-z-]*)?;([^\x07\x1b]*)\x07")
)

// screenProblem reports why a rendered line is unsafe, or "".
func screenProblem(line string) string {
	line = sgr.ReplaceAllString(line, "")
	for _, m := range hyperlinkSeq.FindAllStringSubmatch(line, -1) {
		if m[1] == "" {
			continue // the end of a link
		}
		if target, ok := linkTarget(m[1]); !ok || target != m[1] {
			return fmt.Sprintf("hyperlink to %q, which is no safe address", m[1])
		}
	}
	for _, r := range hyperlinkSeq.ReplaceAllString(line, "") {
		switch {
		case r < 0x20 || r == 0x7f:
			return fmt.Sprintf("C0 control U+%04X", r)
		case r >= 0x80 && r <= 0x9f:
			return fmt.Sprintf("C1 control U+%04X", r)
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return fmt.Sprintf("bidi control U+%04X", r)
		case r == 0x2028 || r == 0x2029:
			return "line or paragraph separator"
		}
	}
	return ""
}

// FuzzRender draws a chat whose text, names, file names, quotes and
// reactions are fuzzed, at a fuzzed width, and checks what reaches the
// screen: every line fits, and nothing but the theme's styling escapes.
func FuzzRender(f *testing.F) {
	for _, s := range synth.Hostile() {
		if len(s) < 1<<12 {
			f.Add(s, s, uint8(60))
		}
	}
	f.Add("", "", uint8(0))
	f.Add("a\x1b]52;c;cHduZWQ=\x07b", "\x1b[2J", uint8(13))
	f.Fuzz(func(t *testing.T, body, name string, width uint8) {
		w := 4 + int(width)%160
		at := testNow.Add(-time.Hour)
		msgs := []domain.Message{
			{ID: 1, ChatID: 1, WAID: "A", SenderID: 1, Kind: domain.KindText, Body: body, Timestamp: at},
			{ID: 2, ChatID: 1, WAID: "B", SenderID: 2, Kind: domain.KindDocument, Body: body, Timestamp: at.Add(time.Minute),
				Media: &domain.Media{MIME: name, FileName: name, Size: 1 << 20}, QuotedWAID: "A",
				Quote:     &domain.Quote{SenderID: 1, Kind: domain.KindText, Body: body},
				Reactions: []domain.ReactionCount{{Emoji: name, Count: 2}}, Mentions: []domain.Mention{{Token: name, Identity: 1}}},
			{ID: 3, ChatID: 1, WAID: "C", FromMe: true, Kind: domain.KindText, Body: body, Status: domain.StatusRead,
				Timestamp: at.Add(2 * time.Minute), EditedAt: at.Add(3 * time.Minute)},
		}
		hist := &memHistory{chats: map[domain.ChatID][]domain.Message{1: msgs}}
		m := New(uikit.NewContext(theme.Default()), hist, NamesFunc(func(domain.IdentityID) string { return name }))
		m.now = func() time.Time { return testNow }
		m.loc = time.UTC
		m.Resize(w, 30)
		drive(m, m.Open(1))
		for i, line := range strings.Split(m.View(), "\n") {
			if got := m.ctx.Width.StringWidth(line); got > w {
				t.Fatalf("width %d: line %d is %d cells wide", w, i, got)
			}
			if p := screenProblem(line); p != "" {
				t.Fatalf("width %d: line %d has a %s: %q", w, i, p, line)
			}
		}
	})
}
