// SPDX-License-Identifier: GPL-3.0-or-later

package composer

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

func newComposer(t *testing.T) *Model {
	t.Helper()
	m := New(uikit.NewContext(theme.Default()))
	m.getenv = func(string) string { return "" }
	m.SetDraftDir(t.TempDir())
	m.Resize(60, 6)
	m.SetFocused(true)
	return m
}

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func act(m *Model, a keymap.Action) tea.Cmd { return m.Update(keymap.ActionMsg{Action: a}) }

// run executes a command chain synchronously, returning the last message.
func run(m *Model, cmd tea.Cmd) tea.Msg {
	var last tea.Msg
	for cmd != nil {
		last = cmd()
		cmd = m.Update(last)
	}
	return last
}

func TestNoChat(t *testing.T) {
	m := newComposer(t)
	typeText(m, "hello")
	if m.Value() != "" {
		t.Fatal("typing without an open chat must be ignored")
	}
	if cmd := act(m, keymap.Send); cmd != nil {
		t.Fatal("sending without a chat must do nothing")
	}
	if !strings.Contains(ansi.Strip(m.View()), "Open a chat") {
		t.Fatalf("view = %q", ansi.Strip(m.View()))
	}
	if m.Cursor() != nil {
		t.Fatal("no cursor without a chat")
	}
}

func TestTypeAndSend(t *testing.T) {
	m := newComposer(t)
	m.SetChat(7)
	typeText(m, "  Merhaba dünya! 👋  ")
	cmd := act(m, keymap.Send)
	if cmd == nil {
		t.Fatal("send returned no command")
	}
	got, ok := cmd().(SendMsg)
	if !ok || got.Chat != 7 || got.Text != "  Merhaba dünya! 👋" {
		t.Fatalf("send = %#v", cmd())
	}
	if m.Value() != "" {
		t.Fatal("the composer must be empty after sending")
	}
	typeText(m, "   ")
	if cmd := act(m, keymap.Send); cmd != nil {
		t.Fatal("whitespace-only messages must not be sent")
	}
}

func TestNewlineGrowsTheComposer(t *testing.T) {
	m := newComposer(t)
	m.SetChat(1)
	if m.Height() != 1 {
		t.Fatalf("empty height = %d, want 1", m.Height())
	}
	typeText(m, "line one")
	act(m, keymap.Newline)
	typeText(m, "line two")
	if m.Value() != "line one\nline two" {
		t.Fatalf("value = %q", m.Value())
	}
	if m.Height() != 2 {
		t.Fatalf("height = %d, want 2", m.Height())
	}
	for range 20 {
		act(m, keymap.Newline)
	}
	if m.Height() != MaxHeight {
		t.Fatalf("height = %d, want the cap %d", m.Height(), MaxHeight)
	}
	got := act(m, keymap.Send)().(SendMsg)
	if got.Text != "line one\nline two" {
		t.Fatalf("trailing newlines must be trimmed, got %q", got.Text)
	}
}

func TestDraftsPerChat(t *testing.T) {
	m := newComposer(t)
	m.SetChat(1)
	typeText(m, "draft for one")
	m.SetChat(2)
	if m.Value() != "" {
		t.Fatal("a new chat must start empty")
	}
	typeText(m, "draft for two")
	m.SetChat(1)
	if m.Value() != "draft for one" {
		t.Fatalf("draft not restored: %q", m.Value())
	}
	act(m, keymap.Send)
	m.SetChat(2)
	m.SetChat(1)
	if m.Value() != "" {
		t.Fatal("a sent draft must not come back")
	}
	m.SetChat(2)
	if m.Value() != "draft for two" {
		t.Fatalf("second draft lost: %q", m.Value())
	}
}

func TestPaste(t *testing.T) {
	m := newComposer(t)
	m.SetChat(1)
	m.Update(tea.PasteMsg{Content: "pasted\ntext"})
	if m.Value() != "pasted\ntext" {
		t.Fatalf("value = %q", m.Value())
	}
}

func TestExternalEditor(t *testing.T) {
	m := newComposer(t)
	m.getenv = func(k string) string {
		if k == "EDITOR" {
			return "myeditor  --wait"
		}
		return ""
	}
	var dir string
	m.exec = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		path := c.Args[len(c.Args)-1]
		if !slices.Equal(c.Args[:2], []string{"myeditor", "--wait"}) || len(c.Args) != 3 {
			t.Errorf("editor argv = %q", c.Args)
		}
		dir = filepath.Dir(path)
		for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
			info, err := os.Stat(p)
			if err != nil || info.Mode().Perm() != want {
				t.Errorf("%s: mode %v, want %v (err %v)", p, info.Mode().Perm(), want, err)
			}
		}
		if data, _ := os.ReadFile(path); string(data) != "draft" { //nolint:gosec // the test's own temp file
			t.Errorf("the draft was not written: %q", data)
		}
		if err := os.WriteFile(path, []byte("edited in vim\nsecond line\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return func() tea.Msg { return fn(nil) }
	}
	m.SetChat(3)
	typeText(m, "draft")
	run(m, act(m, keymap.EditExternal))
	if m.Value() != "edited in vim\nsecond line" {
		t.Fatalf("value after editing = %q", m.Value())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the draft directory was not removed")
	}
}

func TestExternalEditorErrors(t *testing.T) {
	m := newComposer(t)
	m.SetChat(1)
	msg := run(m, act(m, keymap.EditExternal))
	if e, ok := msg.(ErrorMsg); !ok || !strings.Contains(e.Err.Error(), "$EDITOR") {
		t.Fatalf("without an editor: %#v", msg)
	}

	m.getenv = func(k string) string { return map[string]string{"VISUAL": "broken-editor"}[k] }
	var dir string
	m.exec = func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
		dir = filepath.Dir(c.Args[len(c.Args)-1])
		return func() tea.Msg { return fn(errors.New("exit status 1")) }
	}
	typeText(m, "keep me")
	msg = run(m, act(m, keymap.EditExternal))
	if e, ok := msg.(ErrorMsg); !ok || !strings.Contains(e.Err.Error(), "editor: exit status 1") {
		t.Fatalf("failing editor: %#v", msg)
	}
	if m.Value() != "keep me" {
		t.Fatal("a failed edit must keep the draft")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the draft directory was not removed after a failure")
	}

	m.tempDir = func() string { return filepath.Join(t.TempDir(), "missing", "dir") }
	msg = run(m, act(m, keymap.EditExternal))
	if e, ok := msg.(ErrorMsg); !ok || !strings.Contains(e.Err.Error(), "creating draft directory") {
		t.Fatalf("unwritable temp dir: %#v", msg)
	}
}

func TestEditorResultForAnotherChatIsIgnored(t *testing.T) {
	m := newComposer(t)
	m.SetChat(1)
	m.Update(editorResultMsg{text: "late", chat: 2})
	if m.Value() != "" {
		t.Fatal("an editor result for another chat must not land here")
	}
}

func TestCursorAndView(t *testing.T) {
	m := newComposer(t)
	m.SetChat(1)
	typeText(m, "abc")
	c := m.Cursor()
	if c == nil || c.X != 3 || c.Y != 0 {
		t.Fatalf("cursor = %+v, want column 3 of the first line", c)
	}
	m.SetFocused(false)
	if m.Cursor() != nil {
		t.Fatal("an unfocused composer has no cursor")
	}
	if !strings.Contains(ansi.Strip(m.View()), "abc") {
		t.Fatal("the text is not shown")
	}
	m.Resize(0, 0)
	if m.View() != "" {
		t.Fatal("a zero-size composer renders nothing")
	}
}

func TestReply(t *testing.T) {
	m := newComposer(t)
	m.SetChat(1)
	h := m.Height()
	m.SetReply("Q1", "Ali: geliyor\u202e musun?")
	if !m.Replying() || m.Height() != h+1 {
		t.Fatalf("replying %v, height %d", m.Replying(), m.Height())
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "↩ Ali: geliyor musun? · esc cancels") || strings.Contains(view, "\u202e") {
		t.Fatalf("view = %q", view)
	}
	if c := m.Cursor(); c == nil || c.Y != 1 {
		t.Fatalf("the cursor is below the reply line: %+v", c)
	}
	// Each chat keeps its own reply.
	m.SetChat(2)
	if m.Replying() {
		t.Fatal("another chat has no reply")
	}
	m.SetChat(1)
	typeText(m, "evet")
	msg, ok := run(m, act(m, keymap.Send)).(SendMsg)
	if !ok || msg.Text != "evet" || msg.QuotedWAID != "Q1" || m.Replying() {
		t.Fatalf("send = %+v, still replying %v", msg, m.Replying())
	}
	m.SetReply("Q2", "x")
	if !m.ClearReply() || m.Replying() || m.ClearReply() {
		t.Fatal("ClearReply drops the reply once")
	}
	typeText(m, "plain")
	if msg := run(m, act(m, keymap.Send)).(SendMsg); msg.QuotedWAID != "" {
		t.Fatalf("a cleared reply must not be sent: %+v", msg)
	}
	m.SetChat(0)
	m.SetReply("Q3", "x")
	if m.Replying() {
		t.Fatal("no reply without a chat")
	}
}

func TestEdit(t *testing.T) {
	m := newComposer(t)
	m.SetChat(1)
	typeText(m, "draft")
	m.SetReply("Q", "Ali: x")
	m.SetEdit("W1", "eski metin")
	if !m.Editing() || m.Replying() || m.Value() != "eski metin" || m.Height() != 2 {
		t.Fatalf("editing %v, replying %v, value %q, height %d", m.Editing(), m.Replying(), m.Value(), m.Height())
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "✎ editing your message · esc cancels") {
		t.Fatalf("view = %q", v)
	}
	typeText(m, " düzeltildi")
	msg, ok := run(m, act(m, keymap.Send)).(EditMsg)
	if !ok || msg.WAID != "W1" || msg.Text != "eski metin düzeltildi" || m.Editing() || m.Value() != "draft" {
		t.Fatalf("edit = %+v; editing %v; the draft comes back: %q", msg, m.Editing(), m.Value())
	}
	m.SetEdit("W2", "x")
	if !m.CancelEdit() || m.Value() != "draft" || m.CancelEdit() {
		t.Fatal("a canceled edit restores the draft, once")
	}
	m.SetEdit("W3", "y")
	m.SetChat(2)
	m.SetChat(1)
	if m.Editing() || m.Value() != "draft" {
		t.Fatalf("switching chats cancels an edit: editing %v, %q", m.Editing(), m.Value())
	}
}

func TestMentionCompletion(t *testing.T) {
	m := newComposer(t)
	m.SetChat(1)
	if _, ok := m.MentionQuery(); ok {
		t.Fatal("no mention without an @")
	}
	typeText(m, "selam @ay")
	if q, ok := m.MentionQuery(); !ok || q != "ay" {
		t.Fatalf("query = %q, %v", q, ok)
	}
	m.SetSuggestions([]string{"Ayşe Yılmaz", "Ayla"}, 0)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "tab @Ayşe Yılmaz @Ayla") || m.Height() != 2 {
		t.Fatalf("view %q, height %d", v, m.Height())
	}
	m.CompleteMention("Ayşe Yılmaz", "102@lid")
	if m.Value() != "selam @Ayşe Yılmaz " || m.Height() != 1 {
		t.Fatalf("value %q, height %d", m.Value(), m.Height())
	}
	typeText(m, "ve @")
	m.CompleteMention("Ali", "101@lid")
	typeText(m, "bakın")
	msg := run(m, act(m, keymap.Send)).(SendMsg)
	if msg.Text != "selam @102 ve @101 bakın" || !slices.Equal(msg.Mentions, []domain.JID{"102@lid", "101@lid"}) {
		t.Fatalf("send = %+v", msg)
	}
	// A completed mention that was deleted again is not sent.
	typeText(m, "@a")
	m.CompleteMention("Ali", "101@lid")
	m.ta.SetValue("nothing")
	if msg := run(m, act(m, keymap.Send)).(SendMsg); msg.Mentions != nil || msg.Text != "nothing" {
		t.Fatalf("send = %+v", msg)
	}
}
