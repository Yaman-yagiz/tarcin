// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
)

// shownText is a message's text as the list shows it, sanitized, or ""
// if it has none.
func shownText(msg *domain.Message) string {
	if msg.IsRevoked() {
		return ""
	}
	text := sanitize.Text(msg.Body)
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

// copyMessage puts a message's text, as shown, on the clipboard through
// the terminal (OSC 52), which some terminals refuse.
func (m *Model) copyMessage(msg *domain.Message) tea.Cmd {
	text := shownText(msg)
	if text == "" {
		m.notice = "This message has no text to copy"
		return nil
	}
	m.notice = "Copied, if the terminal allows it (OSC 52)"
	return tea.SetClipboard(text)
}

// pagerDoneMsg reports how the pager ended.
type pagerDoneMsg struct{ err error }

// pageMessage shows a message's text, as shown, in $PAGER, or less without
// one. The text goes to the pager's standard input, never to a file, and
// the pager runs without a shell.
func (m *Model) pageMessage(msg *domain.Message) tea.Cmd {
	text := shownText(msg)
	if text == "" {
		m.notice = "This message has no text to read"
		return nil
	}
	argv := strings.Fields(m.getenv("PAGER"))
	if len(argv) == 0 {
		argv = []string{"less"}
	}
	c := exec.Command(argv[0], argv[1:]...) //nolint:gosec,noctx // the user's own $PAGER; Bubble Tea owns its lifetime
	c.Stdin = strings.NewReader(text + "\n")
	return tea.ExecProcess(c, func(err error) tea.Msg { return pagerDoneMsg{err: err} })
}
