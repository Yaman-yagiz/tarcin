// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
)

// AttachResultMsg reports whether an attachment was queued for sending.
type AttachResultMsg struct {
	Chat domain.ChatID
	Err  error
}

// pathCompletionMsg carries a completed :attach path; input is the
// command line it was computed for.
type pathCompletionMsg struct {
	input     string
	completed string
	choices   []string
}

// maxChoices is how many matching names the notice lists.
const maxChoices = 6

// attachCommand runs ":attach PATH": the file goes to the open chat, with
// the composer's text as its caption.
func (m *Model) attachCommand(path string) tea.Cmd {
	chat := m.msgs.Chat()
	switch {
	case m.attachFn == nil:
		m.notice = "sending media is not available"
		return nil
	case chat == 0:
		m.notice = "open a chat to send a file to"
		return nil
	case path == "":
		m.notice = "usage: :attach PATH (tab completes)"
		return nil
	}
	text, quoted, mentions := m.comp.Draft()
	m.notice = "Attaching…"
	cmd := m.attachFn(Outgoing{Chat: chat, Text: text, Quoted: quoted, Mentions: mentions}, path)
	return cmd
}

// attached clears the composer once its text left as a caption.
func (m *Model) attached(msg AttachResultMsg) {
	if msg.Err != nil {
		m.notice = "Not sent: " + msg.Err.Error()
		return
	}
	m.notice = ""
	if msg.Chat == m.comp.Chat() {
		m.comp.Clear()
		m.fitComposer()
	}
}

// completeAttach returns the command that completes the path of an
// ":attach" command line, or nil for any other line.
func completeAttach(input string) tea.Cmd {
	arg, ok := strings.CutPrefix(input, "attach ")
	if !ok {
		return nil
	}
	return func() tea.Msg {
		completed, choices := completePath(strings.TrimLeft(arg, " "))
		return pathCompletionMsg{input: input, completed: "attach " + completed, choices: choices}
	}
}

// completePath extends a path to the longest prefix the names in its
// directory share; a lone directory gets its "/". It keeps the user's
// "~/". Hidden names are offered only when the prefix starts with a dot.
func completePath(arg string) (string, []string) {
	expanded := arg
	if rest, ok := strings.CutPrefix(arg, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, rest) + map[bool]string{true: "/", false: ""}[strings.HasSuffix(arg, "/")]
		}
	}
	dir, base := filepath.Split(expanded)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return arg, nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, base) || (strings.HasPrefix(n, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		if e.IsDir() {
			n += "/"
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return arg, nil
	}
	slices.Sort(names)
	common := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, common) {
			common = common[:len(common)-1]
		}
	}
	return arg[:len(arg)-len(base)] + common, names
}

// completed applies a path completion, if the command line is unchanged.
func (m *Model) completed(msg pathCompletionMsg) {
	if m.mode != modeCommand || m.input.Value() != msg.input {
		return
	}
	m.input.SetValue(msg.completed)
	m.input.CursorEnd()
	m.choices = ""
	if len(msg.choices) > 1 {
		shown := msg.choices[:min(len(msg.choices), maxChoices)]
		m.choices = sanitize.Line(strings.Join(shown, "  ")) // local file names, but any bytes
		if len(msg.choices) > maxChoices {
			m.choices += "  …"
		}
	}
}
