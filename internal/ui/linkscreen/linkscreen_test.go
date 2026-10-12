// SPDX-License-Identifier: GPL-3.0-or-later

package linkscreen

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

type calls struct {
	phone, passkey string
	confirmed      int
	retried        int
}

func newScreen(c *calls) *Model {
	m := New(uikit.NewContext(theme.Default()), Options{
		PairWithPhone: func(phone string) tea.Cmd { c.phone = phone; return func() tea.Msg { return nil } },
		SubmitPasskey: func(r string) tea.Cmd { c.passkey = r; return func() tea.Msg { return nil } },
		ConfirmLink:   func() tea.Cmd { c.confirmed++; return func() tea.Msg { return nil } },
		Retry:         func() tea.Cmd { c.retried++; return func() tea.Msg { return nil } },
	})
	m.Resize(120, 60)
	return m
}

func text(m *Model) string { return ansi.Strip(strings.Join(m.View(), "\n")) }

func press(m *Model, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "ctrl+y":
			msg = tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl}
		default:
			for _, r := range k {
				cmd = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			continue
		}
		cmd = m.Update(msg)
	}
	return cmd
}

func TestQRAndPhoneCode(t *testing.T) {
	c := &calls{}
	m := newScreen(c)
	if !strings.Contains(text(m), "Connecting") {
		t.Fatalf("before the first code:\n%s", text(m))
	}
	m.Progress(domain.LinkProgress{State: domain.LinkQR, QR: "2@" + strings.Repeat("x", 200)})
	view := text(m)
	if !strings.Contains(view, "Linked devices") || !strings.Contains(view, "█") || !strings.Contains(view, "p  link with a phone number") {
		t.Fatalf("QR step:\n%s", view)
	}
	press(m, "p")
	press(m, "+90 555 123 45 67")
	if cmd := press(m, "enter"); cmd == nil || c.phone != "+90 555 123 45 67" {
		t.Fatalf("phone = %q", c.phone)
	}
	// A new QR code does not take the user back.
	m.Progress(domain.LinkProgress{State: domain.LinkQR, QR: "2@next"})
	m.Update(CodeMsg{Code: "ABCD-EFGH"})
	if view := text(m); !strings.Contains(view, "ABCD-EFGH") || !strings.Contains(view, "Link with phone number instead") {
		t.Fatalf("code step:\n%s", view)
	}
	press(m, "esc")
	if !strings.Contains(text(m), "█") {
		t.Fatal("Esc returns to the QR code")
	}
	press(m, "p", "0555")
	press(m, "enter")
	m.Update(CodeMsg{Err: errors.New("give the number in international format")})
	if view := text(m); !strings.Contains(view, "international format") || !strings.Contains(view, "> ") {
		t.Fatalf("a refused number returns to the entry with the reason:\n%s", view)
	}
}

func TestSmallTerminal(t *testing.T) {
	// The size of a real code: a reference, three keys and a secret.
	code := "2@" + strings.Repeat("A", 92) + "," + strings.Repeat("B", 44) + "," + strings.Repeat("C", 44) + "," + strings.Repeat("D", 44)
	qrRows := func(view string) int { return strings.Count(view, "▀") + strings.Count(view, "█") }
	for _, tc := range []struct {
		w, h          int
		full, compact bool
	}{
		{120, 50, true, false}, // the whole screen
		{80, 33, false, true},  // the code and one line
		{80, 24, false, false}, // too small: say so
	} {
		m := newScreen(&calls{})
		m.Resize(tc.w, tc.h)
		m.Progress(domain.LinkProgress{State: domain.LinkQR, QR: code})
		lines := m.View()
		view := text(m)
		if len(lines) > tc.h {
			t.Fatalf("%dx%d: %d lines", tc.w, tc.h, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > tc.w {
				t.Fatalf("%dx%d: line %d is %d wide", tc.w, tc.h, i, w)
			}
		}
		switch {
		case tc.full && (!strings.Contains(view, "Link tarcin") || qrRows(view) == 0):
			t.Fatalf("%dx%d: want the whole screen:\n%s", tc.w, tc.h, view)
		case tc.compact && (strings.Contains(view, "Link tarcin") || qrRows(view) == 0 || !strings.Contains(view, "p  phone number")):
			t.Fatalf("%dx%d: want the code and one line:\n%s", tc.w, tc.h, view)
		case !tc.full && !tc.compact && (qrRows(view) != 0 || !strings.Contains(view, "this one is 80×25") || !strings.Contains(view, "press p")):
			t.Fatalf("%dx%d: a code that does not fit must not be drawn cut off:\n%s", tc.w, tc.h, view)
		}
	}
}

func TestPasskeyHandoff(t *testing.T) {
	c := &calls{}
	m := newScreen(c)
	m.Progress(domain.LinkProgress{State: domain.LinkPasskey, Challenge: `{"challenge":"abc","rpId":"web.whatsapp.com"}`})
	view := text(m)
	for _, want := range []string{"web.whatsapp.com", "parseRequestOptionsFromJSON(", `"rpId":"web.whatsapp.com"`, "console"} {
		if !strings.Contains(view, want) {
			t.Fatalf("passkey step lacks %q:\n%s", want, view)
		}
	}
	if cmd := press(m, "ctrl+y"); cmd == nil {
		t.Fatal("ctrl+y copies the snippet")
	}
	m.Update(tea.PasteMsg{Content: `{"id":"x"}`})
	press(m, "enter")
	if c.passkey != `{"id":"x"}` {
		t.Fatalf("submitted %q", c.passkey)
	}
	m.Update(ResultMsg{Err: errors.New("the passkey response lacks its signature")})
	if !strings.Contains(text(m), "lacks its signature") {
		t.Fatal("a refused response shows why")
	}
	m.Progress(domain.LinkProgress{State: domain.LinkConfirm, Code: "123456"})
	if !strings.Contains(text(m), "123456") {
		t.Fatal("the confirmation code must show")
	}
	press(m, "y")
	if c.confirmed != 1 {
		t.Fatal("y confirms")
	}
	press(m, "n")
	if view := text(m); !strings.Contains(view, "did not match") || !strings.Contains(view, "try again") {
		t.Fatalf("n cancels:\n%s", view)
	}
	press(m, "enter")
	if c.retried != 1 || !strings.Contains(text(m), "Connecting") {
		t.Fatal("Enter retries")
	}
}

func TestServerTextIsSanitized(t *testing.T) {
	m := newScreen(&calls{})
	m.Progress(domain.LinkProgress{State: domain.LinkFailed, Reason: "bad\x1b]0;owned\x07 reason"})
	for _, line := range m.View() {
		if strings.Contains(line, "\x07") || strings.Contains(ansi.Strip(line), "\x1b") {
			t.Fatalf("raw escape in %q", line)
		}
	}
}

func TestWelcomeNotice(t *testing.T) {
	begun := 0
	m := New(uikit.NewContext(theme.Default()), Options{
		Begin:   func() tea.Cmd { begun++; return nil },
		DataDir: "/home/me/.local/share/tarcin/profiles/default",
	})
	m.Resize(80, 40)
	view := text(m)
	for _, want := range []string{"unofficial client", "restricted or banned", "unencrypted", "profiles/default", "full-disk encryption", "Enter"} {
		if !strings.Contains(strings.Join(strings.Fields(view), " "), want) {
			t.Fatalf("the notice must mention %q:\n%s", want, view)
		}
	}
	m.Progress(domain.LinkProgress{State: domain.LinkQR, QR: "2@early"})
	press(m, "p", "esc")
	if !m.Welcoming() || begun != 0 {
		t.Fatalf("only Enter accepts the notice: welcoming %v, begun %d", m.Welcoming(), begun)
	}
	press(m, "enter")
	if m.Welcoming() || begun != 1 || !strings.Contains(text(m), "Connecting") {
		t.Fatalf("after Enter: begun %d\n%s", begun, text(m))
	}
	m.Progress(domain.LinkProgress{State: domain.LinkQR, QR: "2@" + strings.Repeat("x", 200)})
	if !strings.Contains(text(m), "Linked devices") {
		t.Fatalf("linking goes on as usual:\n%s", text(m))
	}
}
