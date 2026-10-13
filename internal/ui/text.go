// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"os/exec"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/ui/clipboard"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
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

// copyMessage puts a message's text, as shown, on the clipboard: with
// wl-copy, xclip or xsel, or else through the terminal (OSC 52).
func (m *Model) copyMessage(msg *domain.Message) tea.Cmd {
	text := shownText(msg)
	if text == "" {
		m.notice = "This message has no text to copy"
		return nil
	}
	return m.copyFn(text)
}

// copied reports how a copy went.
func (m *Model) copied(msg clipboard.ResultMsg) {
	if msg.Tool != "" {
		m.notice = "Copied"
		return
	}
	m.notice = "Sent to the terminal (OSC 52); if it does not paste, install wl-clipboard or xclip"
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

// linkCycle remembers which of a message's links the open-link key
// opens next.
type linkCycle struct {
	msg  domain.MessageID
	next int
}

// openLink opens a link of a message in the browser: the first, and at
// each press the next.
func (m *Model) openLink(msg *domain.Message) tea.Cmd {
	if m.openLinkFn == nil {
		m.notice = "opening links is not available"
		return nil
	}
	links := msglist.Links(shownText(msg))
	if len(links) == 0 {
		m.notice = "This message has no link"
		return nil
	}
	i := 0
	if m.nextLink.msg == msg.ID {
		i = m.nextLink.next % len(links)
	}
	m.nextLink = linkCycle{msg: msg.ID, next: i + 1}
	m.notice = "Opening the link…"
	if len(links) > 1 {
		m.notice = "Opening link " + strconv.Itoa(i+1) + " of " + strconv.Itoa(len(links)) + "; again for the next"
	}
	return m.openLinkFn(links[i])
}

// linkCache remembers whether the selected message has links, so that
// the status line does not search a long message on every frame.
type linkCache struct {
	msg   domain.MessageID
	rev   uint32
	known bool
	links bool
}

func (c *linkCache) has(msg *domain.Message) bool {
	if !c.known || c.msg != msg.ID || c.rev != msg.Revision {
		*c = linkCache{msg: msg.ID, rev: msg.Revision, known: true, links: len(msglist.Links(shownText(msg))) > 0}
	}
	return c.links
}
