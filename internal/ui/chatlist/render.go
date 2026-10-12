// SPDX-License-Identifier: GPL-3.0-or-later

package chatlist

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// Row symbols. They are single-width in every common terminal font.
const (
	symUnread = "●" // black circle
	symTyping = "…" // ellipsis
	symPinned = "▴" // small up triangle
	symMuted  = "~"
)

type cacheKey struct {
	rev      uint32
	width    int
	method   ansi.Method
	theme    string
	label    string // time label: changes as time passes
	selected bool
	focused  bool
}

type cached struct {
	key  cacheKey
	line string
}

// View implements uikit.Pane. It renders only the rows on screen.
func (m *Model) View() string {
	m.rebuild()
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	if len(m.visible) == 0 {
		msg := "No chats"
		if !m.filter.Empty() {
			msg = "No matching chats"
		}
		return m.ctx.Theme.Muted.Render(" " + msg)
	}
	end := min(m.offset+m.h, len(m.visible))
	var b strings.Builder
	now := m.now()
	for i := m.offset; i < end; i++ {
		if i > m.offset {
			b.WriteByte('\n')
		}
		r := m.visible[i]
		b.WriteString(m.renderRow(r, r.ID == m.cursor, now))
	}
	return b.String()
}

func (m *Model) renderRow(r *Row, selected bool, now time.Time) string {
	label := m.label(r, now)
	key := cacheKey{
		rev: r.rev, width: m.w, method: m.ctx.Width, theme: m.ctx.Theme.ID,
		label: label, selected: selected, focused: m.focused && selected,
	}
	m.lookups++
	if c, ok := m.cache[r.ID]; ok && c.key == key {
		return c.line
	}
	m.misses++
	line := m.buildRow(r, label, selected)
	m.cache[r.ID] = &cached{key: key, line: line}
	return line
}

// buildRow lays a row out as: marker, title, then right-aligned meta
// (pinned, muted, unread count, time). A selected row is styled as a
// whole, so its segments are rendered unstyled.
func (m *Model) buildRow(r *Row, label string, selected bool) string {
	t := m.ctx.Theme
	method := m.ctx.Width
	style := func(s lipgloss.Style, text string) string {
		if selected || text == "" {
			return text
		}
		return s.Render(text)
	}

	// Typing is live and the unread count stays visible as a badge, so
	// the typing marker takes precedence over the unread dot.
	marker := " "
	switch {
	case r.Typing:
		marker = style(t.Accent, symTyping)
	case r.Unread > 0 && r.Muted:
		marker = style(t.Muted, symUnread)
	case r.Unread > 0:
		marker = style(t.Accent, symUnread)
	}

	// Right-aligned meta in display order; when space runs out, items are
	// dropped by ascending priority so the unread count survives longest.
	type item struct {
		text     string
		priority int
	}
	var meta []item
	if !r.PinnedAt.IsZero() {
		meta = append(meta, item{style(t.Muted, symPinned), 1})
	}
	if r.Muted {
		meta = append(meta, item{style(t.Muted, symMuted), 2})
	}
	if r.Unread > 0 {
		badge := t.Badge
		if r.Muted {
			badge = t.Muted
		}
		meta = append(meta, item{style(badge, unreadLabel(r.Unread)), 4})
	}
	if label != "" {
		meta = append(meta, item{style(t.Muted, label), 3})
	}
	join := func() string {
		parts := make([]string, len(meta))
		for i, it := range meta {
			parts[i] = it.text
		}
		return strings.Join(parts, " ")
	}

	// marker + space + title + gap + meta
	metaStr := join()
	titleW := m.w - 2 - method.StringWidth(metaStr) - 1
	for titleW < 4 && len(meta) > 0 {
		lowest := 0
		for i, it := range meta {
			if it.priority < meta[lowest].priority {
				lowest = i
			}
		}
		meta = slices.Delete(meta, lowest, lowest+1)
		metaStr = join()
		titleW = m.w - 2 - method.StringWidth(metaStr) - 1
	}
	if len(meta) == 0 {
		titleW = m.w - 2
	}

	title := uikit.Display(method, r.title)
	if title == "" {
		title = "(no name)"
	}
	title = uikit.Truncate(method, title, max(titleW, 0), "…")
	pad := max(titleW-method.StringWidth(title), 0)
	if r.Unread > 0 {
		title = style(t.Unread, title)
	}

	var b strings.Builder
	b.WriteString(marker)
	b.WriteByte(' ')
	b.WriteString(title)
	b.WriteString(strings.Repeat(" ", pad))
	if len(meta) > 0 {
		b.WriteByte(' ')
		b.WriteString(metaStr)
	}
	line := b.String()
	if selected {
		sel := t.Selected
		if m.focused {
			sel = t.SelectedFocused
		}
		// Pad to the full width so the highlight covers the whole row.
		line += strings.Repeat(" ", max(m.w-method.StringWidth(line), 0))
		line = uikit.Truncate(method, line, m.w, "")
		return sel.Render(line)
	}
	if method.StringWidth(line) > m.w { // only in very narrow panes
		line = uikit.Truncate(method, line, m.w, "")
	}
	return line
}

func unreadLabel(n int) string {
	if n > 99 {
		return "99+"
	}
	return strconv.Itoa(n)
}

type labelEntry struct {
	at    time.Time
	today int // calendar day of now when the label was made
	label string
}

// label returns a row's time label, formatting it only when the row's
// activity or the current day changed; labels depend on the calendar day
// only, so the cache stays valid until midnight.
func (m *Model) label(r *Row, now time.Time) string {
	today := dayNumber(now.In(m.loc))
	if e, ok := m.labels[r.ID]; ok && e.today == today && e.at.Equal(r.LastActivity) {
		return e.label
	}
	l := timeLabel(r.LastActivity, now, m.loc)
	m.labels[r.ID] = labelEntry{at: r.LastActivity, today: today, label: l}
	return l
}

// dayNumber counts calendar days, so that day differences ignore the time
// of day and daylight saving changes.
func dayNumber(t time.Time) int {
	y, mo, d := t.Date()
	return int(time.Date(y, mo, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// timeLabel formats a chat's latest activity compactly: the time for
// today, the weekday for the previous six calendar days, the date within
// the year and the year otherwise.
func timeLabel(t, now time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	t, now = t.In(loc), now.In(loc)
	days := dayNumber(now) - dayNumber(t)
	switch {
	case days == 0:
		return t.Format("15:04")
	case days > 0 && days <= 6:
		return t.Format("Mon")
	case t.Year() == now.Year():
		return t.Format("02 Jan")
	}
	return t.Format("2006")
}
