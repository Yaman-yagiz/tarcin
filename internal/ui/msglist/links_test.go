// SPDX-License-Identifier: GPL-3.0-or-later

package msglist

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

const linkedIn = "https://www.linkedin.com/posts/junchengyang_aisystems-aiagents-llmserving-share-7514487644521861120-Ltnd/"

func TestLinks(t *testing.T) {
	for _, tt := range []struct {
		text string
		want []string
	}{
		{"bak: " + linkedIn, []string{linkedIn}},
		{"(see " + linkedIn + ").", []string{linkedIn}},
		{"https://tr.wikipedia.org/wiki/Çay_(içecek), ne güzel", []string{"https://tr.wikipedia.org/wiki/%C3%87ay_%28i%C3%A7ecek%29"}},
		{"*https://example.com/x*", []string{"https://example.com/x"}},
		{"www.example.com/a?b=1&c=2!", []string{"https://www.example.com/a?b=1&c=2"}},
		{"iki: http://a.example/1 ve https://b.example/2.", []string{"http://a.example/1", "https://b.example/2"}},
		{"javascript:alert(1) file:///etc/passwd ftp://x.example", nil},
		{"https://bank.example@evil.example/login", nil}, // a user name hides the real host
		{"https://例え.jp/", nil}, // a host the terminal must show itself
		{"https://" + strings.Repeat("a", 2100) + ".example", nil},
		{"https://", nil},
	} {
		if got := Links(tt.text); !slices.Equal(got, tt.want) {
			t.Errorf("Links(%.60q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

// hyperlinks returns the target of each OSC 8 hyperlink a line opens.
var osc8 = regexp.MustCompile("\x1b\\]8;[^;]*;([^\x1b\a]*)(?:\x1b\\\\|\a)")

func hyperlinks(line string) []string {
	var out []string
	for _, m := range osc8.FindAllStringSubmatch(line, -1) {
		if m[1] != "" {
			out = append(out, m[1])
		}
	}
	return out
}

// TestLinkifyMarksEveryPart: a URL that the wrap breaks over lines is a
// hyperlink to the whole URL on each of them, and the text is unchanged.
func TestLinkifyMarksEveryPart(t *testing.T) {
	para := "Şuna bak " + linkedIn + " güzel yazı"
	for _, width := range []int{20, 37, 60} {
		lines := wrap(ansi.GraphemeWidth, para, width)
		got := linkify(para, lines, "m1p0")
		parts := 0
		for i, l := range got {
			if ansi.Strip(l) != ansi.Strip(lines[i]) {
				t.Fatalf("width %d: line %d text changed: %q", width, i, l)
			}
			links := hyperlinks(l)
			hasURL := strings.ContainsAny(ansi.Strip(l), "/") || strings.Contains(ansi.Strip(l), "linkedin")
			if hasURL && (len(links) != 1 || links[0] != linkedIn) {
				t.Fatalf("width %d: line %q links %q", width, ansi.Strip(l), links)
			}
			parts += len(links)
			if strings.Count(l, "\x1b]8;") != 2*len(links) {
				t.Fatalf("width %d: every hyperlink is closed on its line: %q", width, l)
			}
		}
		if want := len(linkedIn)/width + 1; parts < want {
			t.Fatalf("width %d: %d parts linked, want at least %d", width, parts, want)
		}
	}
	// No URL: the lines come back as they were.
	plain := wrap(ansi.GraphemeWidth, "selam dünya", 5)
	if got := linkify("selam dünya", plain, "x"); !slices.Equal(got, plain) {
		t.Fatalf("got %q", got)
	}
}

// TestRenderedMessageLinks goes through the list's own rendering, with
// the styled ticks of an own message on the link's last line.
func TestRenderedMessageLinks(t *testing.T) {
	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	msg := domain.Message{ID: 1, ChatID: 1, FromMe: true, Timestamp: at, Kind: domain.KindText, Body: "bak " + linkedIn, Status: domain.StatusRead}
	hist := &memHistory{chats: map[domain.ChatID][]domain.Message{1: {msg}}}
	m := New(uikit.NewContext(theme.Default()), hist, nil)
	m.now, m.loc = func() time.Time { return at }, time.UTC
	m.Resize(40, 20)
	drive(m, m.Open(1))
	found := 0
	var text strings.Builder
	for _, l := range strings.Split(m.View(), "\n") {
		for _, target := range hyperlinks(l) {
			if target != linkedIn {
				t.Fatalf("a part links %q", target)
			}
			found++
		}
		text.WriteString(strings.TrimSpace(ansi.Strip(l)))
	}
	if found < 3 || !strings.Contains(text.String(), linkedIn) {
		t.Fatalf("%d linked parts; text %q", found, text.String())
	}
}
