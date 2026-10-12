// SPDX-License-Identifier: GPL-3.0-or-later

// Package search is the message search view: the messages of all chats,
// or of one, that contain every word of a query, newest first, from the
// store's full-text index. Enter opens a result's chat at the message.
package search

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// Limit is how many results a search shows.
const Limit = 200

// Source searches the store. It is called from commands only.
type Source interface {
	Search(ctx context.Context, query string, chat domain.ChatID, limit int) ([]domain.Message, error)
}

// OpenMsg asks the root model to open a chat at a message.
type OpenMsg struct {
	Chat domain.ChatID
	WAID domain.WAMessageID
}

// ResultsMsg delivers a search's results.
type ResultsMsg struct {
	gen   int
	msgs  []domain.Message
	snips []string // each result's text as shown, made once
	err   error
}

// Model is the search view.
type Model struct {
	ctx    *uikit.Context
	src    Source
	names  msglist.Names
	titles func(domain.ChatID) string // chat titles, from the chat list

	gen     int
	query   string
	chat    domain.ChatID // 0: all chats
	results []domain.Message
	snips   []string
	loading bool
	err     error

	sel, offset int
	w, h        int
	loc         *time.Location
}

// New returns a search view.
func New(ctx *uikit.Context, src Source, names msglist.Names, titles func(domain.ChatID) string) *Model {
	return &Model{ctx: ctx, src: src, names: names, titles: titles, loc: time.Local}
}

// SetLocation sets the time zone of the result times.
func (m *Model) SetLocation(loc *time.Location) { m.loc = loc }

// Resize sets the view's size; the header takes two lines.
func (m *Model) Resize(w, h int) { m.w, m.h = w, h }

// Open searches for query, in chat or (0) in all chats.
func (m *Model) Open(query string, chat domain.ChatID) tea.Cmd {
	m.gen++
	m.query, m.chat = query, chat
	m.results, m.snips, m.err, m.loading, m.sel, m.offset = nil, nil, nil, true, 0, 0
	gen, src := m.gen, m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		msgs, err := src.Search(ctx, query, chat, Limit)
		snips := make([]string, len(msgs))
		for i := range msgs {
			snips[i] = resultText(msgs[i].Body, firstWord(query))
		}
		return ResultsMsg{gen: gen, msgs: msgs, snips: snips, err: err}
	}
}

// Update handles results and navigation; the Open action opens the
// selected result.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case ResultsMsg:
		if msg.gen == m.gen {
			m.results, m.snips, m.err, m.loading = msg.msgs, msg.snips, msg.err, false
		}
	case keymap.ActionMsg:
		return m.act(msg.Action)
	}
	return nil
}

func (m *Model) act(a keymap.Action) tea.Cmd {
	page := max(m.h-2, 1)
	switch a {
	case keymap.Down:
		m.sel++
	case keymap.Up:
		m.sel--
	case keymap.Top:
		m.sel = 0
	case keymap.Bottom:
		m.sel = len(m.results) - 1
	case keymap.PageDown:
		m.sel += page
	case keymap.PageUp:
		m.sel -= page
	case keymap.HalfPageDown:
		m.sel += page / 2
	case keymap.HalfPageUp:
		m.sel -= page / 2
	case keymap.Open:
		if m.sel >= 0 && m.sel < len(m.results) {
			r := m.results[m.sel]
			return func() tea.Msg { return OpenMsg{Chat: r.ChatID, WAID: r.WAID} }
		}
		return nil
	}
	m.sel = min(max(m.sel, 0), max(len(m.results)-1, 0))
	switch h := max(m.h-2, 1); {
	case m.sel < m.offset:
		m.offset = m.sel
	case m.sel >= m.offset+h:
		m.offset = m.sel - h + 1
	}
	return nil
}

// View renders the header and the results on screen.
func (m *Model) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	t := m.ctx.Theme
	where := "all chats"
	if m.chat != 0 {
		where = m.title(m.chat)
	}
	head := t.Header.Render("Search “"+uikit.Display(m.ctx.Width, oneLine(m.query))+"”") + t.Muted.Render(" in "+uikit.Display(m.ctx.Width, where))
	status := ""
	switch {
	case m.loading:
		status = "Searching…"
	case m.err != nil:
		status = "The search failed: " + m.err.Error()
	case len(m.results) == 0:
		status = "No messages contain every word"
	case len(m.results) == Limit:
		status = strconv.Itoa(Limit) + " newest results · Enter opens · esc closes"
	default:
		status = strconv.Itoa(len(m.results)) + " results · Enter opens · esc closes"
	}
	lines := []string{head, t.Muted.Render(status)}
	end := min(m.offset+max(m.h-2, 0), len(m.results))
	for i := m.offset; i < end; i++ {
		lines = append(lines, m.row(i))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) title(chat domain.ChatID) string {
	if m.titles != nil {
		if t := m.titles(chat); t != "" {
			return t
		}
	}
	return "a chat"
}

// row is one result: where, who, when, and the text around the first
// word of the query.
func (m *Model) row(i int) string {
	t := m.ctx.Theme
	method := m.ctx.Width
	r := &m.results[i]
	who := "You"
	if !r.FromMe && m.names != nil {
		who = oneLine(m.names.DisplayName(r.SenderID))
	}
	where := ""
	if title := oneLine(m.title(r.ChatID)); m.chat == 0 && title != who {
		where = uikit.Display(method, title) + " · " // a direct chat is named after the sender already
	}
	when := r.Timestamp.In(m.loc).Format("2 Jan 15:04")
	var text string
	if i < len(m.snips) {
		text = uikit.Display(method, m.snips[i])
	}
	if i == m.sel {
		return t.SelectedFocused.Render(uikit.Fit(method, " "+where+who+"  "+when+"  "+text, m.w))
	}
	return " " + t.Accent.Render(where+uikit.Display(method, who)) + "  " + t.Muted.Render(when) + "  " + text
}

func firstWord(q string) string {
	if f := strings.Fields(q); len(f) > 0 {
		return f[0]
	}
	return ""
}

// Bounds of a result's text: the body is read up to resultSource bytes,
// and the row shows resultRunes runes from a little before the match. A
// row is narrower anyway; a huge body must not slow every frame.
const (
	resultSource = 64 << 10
	resultRunes  = 400
)

// resultText is a result's text as its row shows it: one sanitized line
// that starts a little before the first match of word.
func resultText(body, word string) string {
	if len(body) > resultSource {
		body = strings.ToValidUTF8(body[:resultSource], "")
	}
	text := snippet(oneLine(body), word, 30)
	if r := []rune(text); len(r) > resultRunes {
		text = string(r[:resultRunes])
	}
	return text
}

// snippet starts text a little before the first case-insensitive match
// of word, so the match shows in a narrow row.
func snippet(text, word string, before int) string {
	if word == "" {
		return text
	}
	i := strings.Index(strings.ToLower(text), strings.ToLower(word))
	if i <= 0 || len(strings.ToLower(text)) != len(text) {
		return text // no match, or lowercasing moved the bytes
	}
	runes := utf8.RuneCountInString(text[:i])
	if runes <= before {
		return text
	}
	cut := []rune(text)
	return "…" + string(cut[runes-before:])
}

func oneLine(s string) string { return strings.Join(strings.Fields(sanitize.Line(s)), " ") }
