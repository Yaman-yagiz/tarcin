// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/clipboard"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
)

func TestCopyMessage(t *testing.T) {
	m := New(Options{History: emptyHistory{}})
	size(m, 120, 30)
	var copied []string
	tool := "wl-copy"
	m.copyFn = func(text string) tea.Cmd { // never the real clipboard
		copied = append(copied, text)
		return func() tea.Msg { return clipboard.ResultMsg{Tool: tool} }
	}
	msg := domain.Message{ID: 1, ChatID: 1, WAID: "W1", Kind: domain.KindText, Body: "selam \u202edünya\nikinci satır"}
	_, cmd := m.Update(msglist.MessageActionMsg{Action: keymap.Copy, Message: msg})
	runCmd(m, cmd)
	if !reflect.DeepEqual(copied, []string{sanitize.Text(msg.Body)}) {
		t.Fatalf("y copies the text as shown, with the bidi control made visible: %q", copied)
	}
	if s := statusOf(m); !strings.Contains(s, "Copied") || strings.Contains(s, "OSC 52") {
		t.Fatalf("status %q", s)
	}
	// Without a clipboard program, the terminal is asked, and the status
	// says what to install.
	tool = ""
	_, cmd = m.Update(msglist.MessageActionMsg{Action: keymap.Copy, Message: msg})
	runCmd(m, cmd)
	if s := statusOf(m); !strings.Contains(s, "OSC 52") || !strings.Contains(s, "wl-clipboard") {
		t.Fatalf("status %q", s)
	}
	for _, none := range []domain.Message{
		{ID: 2, WAID: "W2", Kind: domain.KindImage},
		{ID: 3, WAID: "W3", Kind: domain.KindText, Body: "gone", RevokedAt: time.Now()},
	} {
		if _, cmd := m.Update(msglist.MessageActionMsg{Action: keymap.Copy, Message: none}); cmd != nil ||
			!strings.Contains(statusOf(m), "no text to copy") {
			t.Fatalf("%s: status %q", none.WAID, statusOf(m))
		}
	}
}

// TestPageMessage runs a real program whose $PAGER copies its standard
// input into a file.
func TestPageMessage(t *testing.T) {
	out := filepath.Join(t.TempDir(), "paged.txt")
	m := New(Options{History: emptyHistory{}})
	m.getenv = func(name string) string {
		if name == "PAGER" {
			return "cp /dev/stdin " + out
		}
		return ""
	}
	in, keys := io.Pipe()
	defer func() { _ = keys.Close() }()
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(io.Discard), tea.WithWindowSize(80, 20))
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	msg := domain.Message{ID: 1, ChatID: 1, WAID: "W1", Kind: domain.KindText, Body: "uzun mesaj \u202e"}
	p.Send(msglist.MessageActionMsg{Action: keymap.ViewPager, Message: msg})
	deadline := time.Now().Add(10 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(out); err == nil && len(b) > 0 { //nolint:gosec // the test's file
			got = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.Quit()
	<-done
	if want := sanitize.Text(msg.Body) + "\n"; string(got) != want {
		t.Fatalf("the pager read %q, want %q", got, want)
	}
}

func TestStatusLineNamesTheMediaKeys(t *testing.T) {
	now := time.Now()
	hist := genHistory{1: {
		{ID: 1, ChatID: 1, WAID: "T", SenderID: 2, Kind: domain.KindText, Body: "hi", Timestamp: now.Add(-time.Minute)},
		{ID: 2, ChatID: 1, WAID: "I", SenderID: 2, Kind: domain.KindImage, Media: &domain.Media{MIME: "image/jpeg", Size: 1 << 20}, Timestamp: now},
	}}
	m := New(Options{History: hist, OpenMedia: func(domain.Message) tea.Cmd { return nil }, SaveMedia: func(domain.Message) tea.Cmd { return nil }})
	size(m, 120, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 1, Title: "Aile", LastActivity: now}}})
	_, open := m.Update(key("enter"))
	runCmd(m, open)
	if m.Focus() != FocusMessages {
		m.Update(key("tab"))
	}
	if st := statusOf(m); !strings.Contains(st, "o open · s save") {
		t.Fatalf("status %q: an image is selected", st)
	}
	m.Update(key("k"))
	if st := statusOf(m); strings.Contains(st, "o open") {
		t.Fatalf("status %q: a text message is selected", st)
	}
}

// TestOpenLink: gx opens a message's links one after another, and the
// status line names the key while a message with a link is selected.
func TestOpenLink(t *testing.T) {
	now := time.Now()
	hist := genHistory{1: {
		{ID: 1, ChatID: 1, WAID: "T", SenderID: 2, Kind: domain.KindText, Body: "no link here", Timestamp: now.Add(-time.Minute)},
		{ID: 2, ChatID: 1, WAID: "L", SenderID: 2, Kind: domain.KindText, Timestamp: now,
			Body: "bak https://a.example/1 ve www.b.example/2."},
	}}
	var opened []string
	m := New(Options{History: hist, OpenLink: func(u string) tea.Cmd { opened = append(opened, u); return nil }})
	size(m, 120, 30)
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 1, Title: "Aile", LastActivity: now}}})
	_, open := m.Update(key("enter"))
	runCmd(m, open)
	if m.Focus() != FocusMessages {
		m.Update(key("tab"))
	}
	if st := statusOf(m); !strings.Contains(st, "g x open link") {
		t.Fatalf("status %q: a message with a link is selected", st)
	}
	gx := func() {
		for _, k := range []string{"g", "x"} {
			_, cmd := m.Update(key(k))
			runCmd(m, cmd)
		}
	}
	for range 3 {
		gx()
	}
	want := []string{"https://a.example/1", "https://www.b.example/2", "https://a.example/1"}
	if !reflect.DeepEqual(opened, want) {
		t.Fatalf("opened %q, want %q", opened, want)
	}
	m.Update(key("k"))
	if st := statusOf(m); strings.Contains(st, "open link") {
		t.Fatalf("status %q: a message without a link is selected", st)
	}
	gx()
	if st := statusOf(m); !strings.Contains(st, "no link") || len(opened) != 3 {
		t.Fatalf("status %q, opened %q", st, opened)
	}
}
