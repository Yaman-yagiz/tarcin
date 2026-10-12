// SPDX-License-Identifier: GPL-3.0-or-later

// Package members is the members view of a group: a full-screen list of
// its participants with their roles. A group may have thousands of
// members, so the list is read in pages as the selection nears the end
// of what is loaded, and only the rows on screen are rendered.
package members

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// PageSize is how many members one read loads.
const PageSize = 200

// Source reads a group from storage. Its methods are called from
// commands, never from Update or View.
type Source interface {
	GroupMeta(ctx context.Context, chat domain.ChatID) (domain.GroupMeta, bool, error)
	GroupMemberCount(ctx context.Context, chat domain.ChatID) (int, error)
	GroupMembers(ctx context.Context, chat domain.ChatID, offset, limit int) ([]domain.GroupMemberRow, error)
}

// InfoMsg delivers a group's metadata and member count.
type InfoMsg struct {
	Chat  domain.ChatID
	Gen   int
	Meta  domain.GroupMeta
	Count int
	Err   error
}

// PageMsg delivers up to Limit members starting at Offset.
type PageMsg struct {
	Chat          domain.ChatID
	Gen           int
	Offset, Limit int
	Rows          []domain.GroupMemberRow
	Err           error
}

// Model is the members view.
type Model struct {
	ctx   *uikit.Context
	src   Source
	names msglist.Names

	chat    domain.ChatID
	title   string
	gen     int // advances on every open and reload, to drop stale pages
	meta    domain.GroupMeta
	count   int
	rows    []domain.GroupMemberRow
	end     bool // every member is loaded
	loading bool
	problem string

	sel, offset int
	wantBottom  bool // keep loading and selecting the last row
	w, h        int
}

// New returns a members view reading from src.
func New(ctx *uikit.Context, src Source, names msglist.Names) *Model {
	return &Model{ctx: ctx, src: src, names: names}
}

// Chat returns the group shown, or 0.
func (m *Model) Chat() domain.ChatID { return m.chat }

// Open shows a group's members from the start.
func (m *Model) Open(chat domain.ChatID, title string) tea.Cmd {
	*m = Model{ctx: m.ctx, src: m.src, names: m.names, chat: chat, title: title, gen: m.gen + 1, w: m.w, h: m.h}
	return tea.Batch(m.info(), m.load())
}

// Reload reads the group again, keeping the selection where it can: its
// members or metadata changed.
func (m *Model) Reload() tea.Cmd {
	if m.chat == 0 {
		return nil
	}
	m.gen++
	m.rows, m.end, m.loading = nil, false, false
	return tea.Batch(m.info(), m.loadTo(max(m.sel+1, PageSize)))
}

// Resize sets the view's size; the header takes three lines.
func (m *Model) Resize(w, h int) { m.w, m.h = w, h }

func (m *Model) info() tea.Cmd {
	chat, gen, src := m.chat, m.gen, m.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		meta, _, err := src.GroupMeta(ctx, chat)
		if err != nil {
			return InfoMsg{Chat: chat, Gen: gen, Err: err}
		}
		n, err := src.GroupMemberCount(ctx, chat)
		return InfoMsg{Chat: chat, Gen: gen, Meta: meta, Count: n, Err: err}
	}
}

// load reads the next page, unless one is on its way or all are loaded.
func (m *Model) load() tea.Cmd { return m.loadTo(len(m.rows) + PageSize) }

// loadTo reads members from the end of what is loaded up to limit.
func (m *Model) loadTo(limit int) tea.Cmd {
	if m.loading || m.end || m.src == nil {
		return nil
	}
	m.loading = true
	chat, gen, src, offset := m.chat, m.gen, m.src, len(m.rows)
	n := max(limit-offset, PageSize)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, err := src.GroupMembers(ctx, chat, offset, n)
		return PageMsg{Chat: chat, Gen: gen, Offset: offset, Limit: n, Rows: rows, Err: err}
	}
}

// Update handles navigation actions and loaded data.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case InfoMsg:
		if msg.Chat != m.chat || msg.Gen != m.gen {
			return nil
		}
		if msg.Err != nil {
			m.problem = "Could not read the group: " + msg.Err.Error()
			return nil
		}
		m.meta, m.count = msg.Meta, msg.Count
	case PageMsg:
		if msg.Chat != m.chat || msg.Gen != m.gen || msg.Offset != len(m.rows) {
			return nil
		}
		m.loading = false
		if msg.Err != nil {
			m.problem = "Could not read the members: " + msg.Err.Error()
			return nil
		}
		m.rows = append(m.rows, msg.Rows...)
		m.end = len(msg.Rows) < msg.Limit || (m.count > 0 && len(m.rows) >= m.count)
		if m.wantBottom {
			m.sel = max(len(m.rows)-1, 0)
			if m.end {
				m.wantBottom = false
			}
		}
		m.clamp()
		return m.more()
	case keymap.ActionMsg:
		return m.act(msg.Action)
	}
	return nil
}

func (m *Model) act(a keymap.Action) tea.Cmd {
	page := max(m.listHeight(), 1)
	switch a {
	case keymap.Down:
		m.sel++
	case keymap.Up:
		m.sel--
	case keymap.Top:
		m.sel, m.wantBottom = 0, false
	case keymap.Bottom:
		m.sel = len(m.rows) - 1
		m.wantBottom = !m.end
	case keymap.PageDown:
		m.sel += page
	case keymap.PageUp:
		m.sel -= page
	case keymap.HalfPageDown:
		m.sel += page / 2
	case keymap.HalfPageUp:
		m.sel -= page / 2
	default:
		return nil
	}
	m.clamp()
	return m.more()
}

// more loads the next page once the selection is within a screen of the
// end of what is loaded.
func (m *Model) more() tea.Cmd {
	if m.wantBottom || m.sel >= len(m.rows)-max(m.listHeight(), 1) {
		return m.load()
	}
	return nil
}

func (m *Model) clamp() {
	m.sel = min(max(m.sel, 0), max(len(m.rows)-1, 0))
	h := max(m.listHeight(), 1)
	switch {
	case m.sel < m.offset:
		m.offset = m.sel
	case m.sel >= m.offset+h:
		m.offset = m.sel - h + 1
	}
	m.offset = max(min(m.offset, len(m.rows)-h), 0)
}

// listHeight is the height left for rows below the header.
func (m *Model) listHeight() int { return m.h - 3 }

// View renders the header and the rows on screen.
func (m *Model) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	t := m.ctx.Theme
	method := m.ctx.Width
	count := m.count
	if count == 0 {
		count = len(m.rows)
	}
	head := uikit.Display(method, sanitize.Line(m.title))
	lines := []string{
		t.Header.Render(head) + t.Muted.Render(" · "+strconv.Itoa(count)+" members"),
		t.Muted.Render(m.subtitle()),
		"",
	}
	switch {
	case m.problem != "":
		lines = append(lines, t.Warning.Render(m.problem))
	case len(m.rows) == 0 && m.loading:
		lines = append(lines, t.Muted.Render("Loading…"))
	case len(m.rows) == 0:
		lines = append(lines, t.Muted.Render("No members are known yet."))
	}
	end := min(m.offset+max(m.listHeight(), 0), len(m.rows))
	for i := m.offset; i < end; i++ {
		lines = append(lines, m.row(i))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) subtitle() string {
	method := m.ctx.Width
	var parts []string
	if topic := strings.Join(strings.Fields(sanitize.Line(m.meta.Topic)), " "); topic != "" {
		parts = append(parts, uikit.Display(method, topic))
	}
	if m.meta.AnnounceOnly {
		parts = append(parts, "only admins send")
	}
	if m.meta.Locked {
		parts = append(parts, "only admins edit the group")
	}
	if len(parts) == 0 {
		return "esc closes"
	}
	return strings.Join(parts, " · ")
}

func (m *Model) row(i int) string {
	t := m.ctx.Theme
	method := m.ctx.Width
	r := m.rows[i]
	name := uikit.Display(method, sanitize.Line(m.names.DisplayName(r.Identity)))
	if name == "" {
		name = "unknown"
	}
	role := ""
	switch r.Role {
	case domain.RoleSuperAdmin:
		role = "owner"
	case domain.RoleAdmin:
		role = "admin"
	}
	if i == m.sel {
		line := " " + name
		if role != "" {
			line += "  " + role
		}
		return t.SelectedFocused.Render(uikit.Fit(method, line, m.w))
	}
	line := " " + t.Sender(int64(r.Identity)).Render(name)
	if role != "" {
		line += "  " + t.Accent.Render(role)
	}
	return line
}
