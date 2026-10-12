// SPDX-License-Identifier: GPL-3.0-or-later

// Package composer is the message input: a multi-line text area that
// grows with its content, keeps a draft per chat, and can hand the draft
// to the user's editor (docs/architecture/ui.md §5–§6).
package composer

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

const (
	// MaxLength is the longest text message WhatsApp accepts, in runes.
	MaxLength = 65536
	// MaxHeight is the tallest the composer grows, in lines.
	MaxHeight = 6
)

// SendMsg asks the root model to send a text message, as a reply to
// QuotedWAID if it is set.
type SendMsg struct {
	Chat       domain.ChatID
	Text       string
	QuotedWAID domain.WAMessageID
	// Mentions are the people Text mentions, each as "@" and the user
	// part of its JID.
	Mentions []domain.JID
}

// EditMsg asks the root model to replace the text of this account's
// message WAID.
type EditMsg struct {
	Chat domain.ChatID
	WAID domain.WAMessageID
	Text string
}

// ErrorMsg reports a problem to show in the status line.
type ErrorMsg struct {
	Err error
}

// editorReadyMsg, editorDoneMsg and editorResultMsg carry the external
// editor round trip through commands, so no file I/O runs in Update.
type editorReadyMsg struct {
	dir, path string
	argv      []string
	chat      domain.ChatID
	err       error
}

type editorDoneMsg struct {
	dir, path string
	chat      domain.ChatID
	err       error
}

type editorResultMsg struct {
	text string
	chat domain.ChatID
	err  error
}

// Model is the composer. Create it with New.
type Model struct {
	ctx     *uikit.Context
	ta      textarea.Model
	w, h    int
	focused bool

	chat     domain.ChatID
	drafts   map[domain.ChatID]string
	replies  map[domain.ChatID]reply
	edit     *editing // the open chat's message being edited
	mentions map[domain.ChatID][]mention
	// suggest is the line of mention suggestions, empty for none; the
	// root model fills it.
	suggest []string
	pick    int

	// getenv, tempDir and exec are replaceable in tests.
	getenv  func(string) string
	tempDir func() string
	exec    func(c *exec.Cmd, fn tea.ExecCallback) tea.Cmd
}

// editing is an edit of one of this account's messages, with the draft
// it put aside.
type editing struct {
	waid  domain.WAMessageID
	draft string
}

// mention is a completed mention: the name inserted, and whom it names.
type mention struct {
	text string // "@Name"
	jid  domain.JID
}

// reply is the message the next send in a chat answers.
type reply struct {
	waid    domain.WAMessageID
	preview string // "Name: text", raw: sanitized when shown
}

// New returns a composer.
func New(ctx *uikit.Context) *Model {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = MaxLength
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = MaxHeight
	ta.Placeholder = "Message (Enter to send, Alt-Enter for a new line)"
	ta.SetVirtualCursor(false)
	// Enter belongs to the keymap (send); newlines arrive as actions.
	ta.KeyMap.InsertNewline.SetEnabled(false)

	// Only the theme colors anything: the textarea's defaults would color
	// the prompt and the selection, and the terminal's cursor.
	s := ta.Styles()
	for _, st := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		st.Base = lipgloss.NewStyle()
		st.Text = lipgloss.NewStyle()
		st.CursorLine = lipgloss.NewStyle()
		st.CursorLineNumber = lipgloss.NewStyle()
		st.LineNumber = lipgloss.NewStyle()
		st.Prompt = lipgloss.NewStyle()
		st.Selection = lipgloss.NewStyle().Reverse(true)
		st.Placeholder = ctx.Theme.Muted
		st.EndOfBuffer = lipgloss.NewStyle()
	}
	s.Cursor.Color = nil
	ta.SetStyles(s)

	return &Model{
		ctx:      ctx,
		ta:       ta,
		drafts:   map[domain.ChatID]string{},
		replies:  map[domain.ChatID]reply{},
		mentions: map[domain.ChatID][]mention{},
		getenv:   os.Getenv,
		tempDir:  defaultTempDir,
		exec:     tea.ExecProcess,
	}
}

// SetDraftDir sets the private directory where drafts handed to the
// editor are written.
func (m *Model) SetDraftDir(dir string) { m.tempDir = func() string { return dir } }

// defaultTempDir prefers the per-user runtime directory, which is private
// (0700) and usually in memory, for drafts handed to the editor.
func defaultTempDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(d) {
		return d
	}
	return os.TempDir()
}

// SetChat switches the composer to a chat, saving the current draft and
// restoring the new chat's draft.
func (m *Model) SetChat(chat domain.ChatID) {
	if chat == m.chat {
		return
	}
	m.CancelEdit()
	if m.chat != 0 {
		if v := m.ta.Value(); v != "" {
			m.drafts[m.chat] = v
		} else {
			delete(m.drafts, m.chat)
		}
	}
	m.chat = chat
	m.ta.SetValue(m.drafts[chat])
	m.ta.CursorEnd()
}

// Chat returns the chat the composer writes to, or 0.
func (m *Model) Chat() domain.ChatID { return m.chat }

// Value returns the current text.
func (m *Model) Value() string { return m.ta.Value() }

// SetEdit puts the text of this account's message waid in the composer
// for editing; the draft is put aside until the edit is sent or
// canceled. An edit replaces a reply.
func (m *Model) SetEdit(waid domain.WAMessageID, body string) {
	if m.chat == 0 || waid == "" {
		return
	}
	draft := m.ta.Value()
	if m.edit != nil {
		draft = m.edit.draft
	}
	delete(m.replies, m.chat)
	m.edit = &editing{waid: waid, draft: draft}
	m.ta.SetValue(body)
	m.ta.CursorEnd()
}

// CancelEdit ends an edit without sending it, restoring the draft, and
// reports whether there was one.
func (m *Model) CancelEdit() bool {
	if m.edit == nil {
		return false
	}
	m.ta.SetValue(m.edit.draft)
	m.ta.CursorEnd()
	m.edit = nil
	return true
}

// Editing reports whether the composer edits a sent message.
func (m *Model) Editing() bool { return m.edit != nil }

// SetReply makes the next message in the current chat a reply to waid;
// preview describes that message. Each chat keeps its own, like drafts.
func (m *Model) SetReply(waid domain.WAMessageID, preview string) {
	if m.chat != 0 && waid != "" {
		m.CancelEdit()
		m.replies[m.chat] = reply{waid: waid, preview: preview}
	}
}

// ClearReply drops the current chat's reply and reports whether there
// was one.
func (m *Model) ClearReply() bool {
	_, ok := m.replies[m.chat]
	delete(m.replies, m.chat)
	return ok
}

// Replying reports whether the next message is a reply.
func (m *Model) Replying() bool {
	_, ok := m.replies[m.chat]
	return ok && m.chat != 0
}

// MentionQuery returns the mention being typed: the text after an "@"
// that starts the last word, while nothing follows it.
func (m *Model) MentionQuery() (string, bool) {
	if m.chat == 0 {
		return "", false
	}
	v := m.ta.Value()
	i := strings.LastIndexAny(v, " \t\n")
	word := v[i+1:]
	if !strings.HasPrefix(word, "@") {
		return "", false
	}
	return word[1:], true
}

// CompleteMention replaces the mention being typed with "@name " and
// remembers whom it names, for the send.
func (m *Model) CompleteMention(name string, jid domain.JID) {
	q, ok := m.MentionQuery()
	if !ok || jid.IsZero() {
		return
	}
	v := m.ta.Value()
	m.ta.SetValue(v[:len(v)-len(q)-1] + "@" + name + " ")
	m.ta.CursorEnd()
	m.mentions[m.chat] = append(m.mentions[m.chat], mention{text: "@" + name, jid: jid})
	m.suggest = nil
}

// SetSuggestions shows the names a mention can complete to, pick being
// the one Tab takes; none hides the line.
func (m *Model) SetSuggestions(names []string, pick int) { m.suggest, m.pick = names, pick }

// Height returns the number of lines the composer wants to occupy: the
// text, the reply or edit line above it, and the suggestions.
func (m *Model) Height() int {
	h := min(max(m.ta.Height(), 1), MaxHeight)
	if m.Replying() || m.Editing() {
		h++
	}
	if len(m.suggest) > 0 {
		h++
	}
	return h
}

// Cursor returns the terminal cursor relative to the composer, or nil.
func (m *Model) Cursor() *tea.Cursor {
	if !m.focused || m.chat == 0 {
		return nil
	}
	c := m.ta.Cursor()
	if c != nil && (m.Replying() || m.Editing()) {
		c.Y++
	}
	if c != nil && len(m.suggest) > 0 {
		c.Y++
	}
	return c
}

// Resize implements uikit.Pane.
func (m *Model) Resize(w, h int) {
	m.w, m.h = w, h
	m.ta.SetWidth(max(w, 1))
}

// SetFocused implements uikit.Pane.
func (m *Model) SetFocused(focused bool) {
	m.focused = focused
	if focused {
		m.ta.Focus()
	} else {
		m.ta.Blur()
	}
}

// Update implements uikit.Pane.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case keymap.ActionMsg:
		return m.action(msg.Action)
	case editorReadyMsg:
		return m.runEditor(msg)
	case editorDoneMsg:
		return readDraft(msg)
	case editorResultMsg:
		if msg.err != nil {
			return errCmd(msg.err)
		}
		if msg.chat == m.chat {
			m.ta.SetValue(strings.TrimRight(msg.text, "\n"))
			m.ta.CursorEnd()
		}
		return nil
	case tea.KeyPressMsg, tea.PasteMsg:
		if m.chat == 0 {
			return nil
		}
		var cmd tea.Cmd
		m.ta, cmd = m.ta.Update(msg)
		return cmd
	}
	return nil
}

func (m *Model) action(a keymap.Action) tea.Cmd {
	if m.chat == 0 {
		return nil
	}
	switch a {
	case keymap.Send:
		text := strings.TrimRight(m.ta.Value(), " \t\r\n")
		text = strings.TrimLeft(text, "\r\n")
		if strings.TrimSpace(text) == "" {
			return nil
		}
		chat, quoted := m.chat, m.replies[m.chat].waid
		m.suggest = nil
		if e := m.edit; e != nil {
			// An edit sends text only: WhatsApp keeps the mentions of the
			// original message.
			m.edit = nil
			m.ta.SetValue(e.draft)
			m.ta.CursorEnd()
			return func() tea.Msg { return EditMsg{Chat: chat, WAID: e.waid, Text: text} }
		}
		text, mentioned := m.encodeMentions(text)
		delete(m.mentions, chat)
		m.ta.Reset()
		delete(m.drafts, chat)
		delete(m.replies, chat)
		return func() tea.Msg { return SendMsg{Chat: chat, Text: text, QuotedWAID: quoted, Mentions: mentioned} }
	case keymap.Newline:
		m.ta.InsertString("\n")
	case keymap.EditExternal:
		return m.prepareEditor()
	}
	return nil
}

// Draft returns what a send would: the text with its mentions encoded,
// the message it answers, and the people it mentions, without clearing
// anything (an attachment's caption).
func (m *Model) Draft() (string, domain.WAMessageID, []domain.JID) {
	text := strings.TrimSpace(m.ta.Value())
	text, mentioned := m.encodeMentions(text)
	return text, m.replies[m.chat].waid, mentioned
}

// Clear empties the composer of the current chat: text, reply, edit and
// mentions.
func (m *Model) Clear() {
	m.edit = nil
	m.ta.Reset()
	delete(m.drafts, m.chat)
	delete(m.replies, m.chat)
	delete(m.mentions, m.chat)
	m.suggest = nil
}

// encodeMentions turns each completed mention still in text, "@Name",
// into "@" and the user part of its JID, as WhatsApp sends them, and
// returns the people it names.
func (m *Model) encodeMentions(text string) (string, []domain.JID) {
	var out []domain.JID
	for _, mn := range m.mentions[m.chat] {
		i := strings.Index(text, mn.text)
		if i < 0 {
			continue // edited away
		}
		text = text[:i] + "@" + mn.jid.User() + text[i+len(mn.text):]
		if !slices.Contains(out, mn.jid) {
			out = append(out, mn.jid)
		}
	}
	return text, out
}

// editorCommand returns the user's editor as an argument vector. It is
// split on whitespace and run without a shell, so the draft path is never
// interpreted by one.
func (m *Model) editorCommand() ([]string, error) {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if argv := strings.Fields(m.getenv(v)); len(argv) > 0 {
			return argv, nil
		}
	}
	return nil, errors.New("set $VISUAL or $EDITOR to edit messages in an editor")
}

// prepareEditor writes the draft to a private temporary file (directory
// 0700, file 0600) in a command and reports the path back.
func (m *Model) prepareEditor() tea.Cmd {
	argv, err := m.editorCommand()
	if err != nil {
		return errCmd(err)
	}
	draft, chat, base := m.ta.Value(), m.chat, m.tempDir()
	return func() tea.Msg {
		dir, err := os.MkdirTemp(base, "tarcin-draft-")
		if err != nil {
			return editorReadyMsg{err: fmt.Errorf("creating draft directory: %w", err)}
		}
		path := filepath.Join(dir, "message.txt")
		if err := os.WriteFile(path, []byte(draft), 0o600); err != nil {
			_ = os.RemoveAll(dir)
			return editorReadyMsg{err: fmt.Errorf("writing draft: %w", err)}
		}
		return editorReadyMsg{dir: dir, path: path, argv: argv, chat: chat}
	}
}

func (m *Model) runEditor(msg editorReadyMsg) tea.Cmd {
	if msg.err != nil {
		return errCmd(msg.err)
	}
	args := append(slices.Clone(msg.argv[1:]), msg.path)
	// The user's own $EDITOR, run without a shell. No context: Bubble Tea
	// suspends the UI and owns the interactive process's lifetime, and a
	// canceled context must not kill an editor with unsaved work.
	c := exec.Command(msg.argv[0], args...) //nolint:gosec,noctx // see above
	return m.exec(c, func(err error) tea.Msg {
		return editorDoneMsg{dir: msg.dir, path: msg.path, chat: msg.chat, err: err}
	})
}

// readDraft reads the edited draft and removes the temporary directory.
func readDraft(msg editorDoneMsg) tea.Cmd {
	return func() tea.Msg {
		defer os.RemoveAll(msg.dir)
		if msg.err != nil {
			return editorResultMsg{err: fmt.Errorf("editor: %w", msg.err)}
		}
		data, err := os.ReadFile(msg.path)
		if err != nil {
			return editorResultMsg{err: fmt.Errorf("reading draft: %w", err)}
		}
		text := string(data)
		if r := []rune(text); len(r) > MaxLength {
			text = string(r[:MaxLength])
		}
		return editorResultMsg{text: text, chat: msg.chat}
	}
}

func errCmd(err error) tea.Cmd {
	return func() tea.Msg { return ErrorMsg{Err: err} }
}

// View implements uikit.Pane.
func (m *Model) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	if m.chat == 0 {
		return m.ctx.Theme.Muted.Render("Open a chat to write a message")
	}
	// The text area measures graphemes; show what wcwidth terminals agree on.
	text := uikit.Display(m.ctx.Width, m.ta.View())
	if len(m.suggest) > 0 {
		parts := make([]string, len(m.suggest))
		for i, n := range m.suggest {
			n = "@" + uikit.Display(m.ctx.Width, sanitize.Line(n))
			if i == m.pick {
				parts[i] = m.ctx.Theme.SelectedFocused.Render(n)
			} else {
				parts[i] = m.ctx.Theme.Muted.Render(n)
			}
		}
		text = m.ctx.Theme.Muted.Render("tab ") + strings.Join(parts, " ") + "\n" + text
	}
	if m.edit != nil {
		text = m.ctx.Theme.Accent.Render("✎ ") + m.ctx.Theme.Muted.Render("editing your message · esc cancels") + "\n" + text
	} else if r, ok := m.replies[m.chat]; ok {
		preview := strings.Join(strings.Fields(sanitize.Line(r.preview)), " ")
		line := m.ctx.Theme.Accent.Render("↩ ") + m.ctx.Theme.Muted.Render(uikit.Display(m.ctx.Width, preview)+" · esc cancels")
		text = line + "\n" + text
	}
	return text
}
