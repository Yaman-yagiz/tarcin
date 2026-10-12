// SPDX-License-Identifier: GPL-3.0-or-later

package msglist

// Scrolling works on the anchor: the bottom-most visible message and the
// number of its lines hidden below the viewport (skip). Heights come from
// the render cache, so a scroll costs O(lines moved), not O(history).

// atBottom reports whether the newest window message is fully visible at
// the bottom of the viewport.
func (m *Model) atBottom() bool {
	n := len(m.win)
	return n > 0 && m.anchor == m.win[n-1].ID && m.skip == 0
}

// scrollUp reveals n older lines.
func (m *Model) scrollUp(n int) {
	if len(m.win) == 0 || n <= 0 {
		return
	}
	m.follow = false
	m.skip += n
	for {
		i := m.idx[m.anchor]
		h := m.height(i)
		if m.skip < h {
			break
		}
		if i == 0 {
			m.skip = h - 1
			break
		}
		m.skip -= h
		m.anchor = m.win[i-1].ID
	}
	m.clampAnchor()
}

// scrollDown reveals n newer lines.
func (m *Model) scrollDown(n int) {
	if len(m.win) == 0 || n <= 0 {
		return
	}
	m.skip -= n
	for m.skip < 0 {
		i := m.idx[m.anchor]
		if i == len(m.win)-1 {
			m.skip = 0
			break
		}
		m.anchor = m.win[i+1].ID
		m.skip += m.height(i + 1)
	}
	if m.newerDone && m.atBottom() {
		m.follow, m.unseen = true, 0
	}
	m.clampAnchor()
}

// clampAnchor keeps the anchor valid and the viewport filled: it never
// shows blank lines above the first window message unless everything
// fits on screen, in which case the messages sit at the bottom.
func (m *Model) clampAnchor() {
	n := len(m.win)
	if n == 0 {
		m.anchor, m.skip = 0, 0
		return
	}
	i, ok := m.idx[m.anchor]
	if !ok {
		m.anchor, m.skip = m.win[n-1].ID, 0
		i = n - 1
	}
	m.skip = min(max(m.skip, 0), m.height(i)-1)
	if m.h <= 0 || m.atBottom() {
		return
	}
	if _, _, filled := m.topVisible(); filled < m.h {
		deficit := m.h - filled
		m.skip -= deficit
		for m.skip < 0 && i < n-1 {
			i++
			m.anchor = m.win[i].ID
			m.skip += m.height(i)
		}
		m.skip = max(m.skip, 0)
	}
}

// topVisible walks up from the anchor and returns the index of the
// top-most visible message, how many of its lines are cut above the
// viewport, and how many lines are filled (at most h).
func (m *Model) topVisible() (top, cut, filled int) {
	if len(m.win) == 0 {
		return 0, 0, 0
	}
	i := m.idx[m.anchor]
	filled = m.height(i) - m.skip
	for filled < m.h && i > 0 {
		i--
		filled += m.height(i)
	}
	if filled > m.h {
		return i, filled - m.h, m.h
	}
	return i, 0, filled
}

// moveSelection selects the message delta positions away and scrolls it
// into view.
func (m *Model) moveSelection(delta int) {
	i, ok := m.idx[m.selected]
	if !ok {
		i = m.idx[m.anchor]
	}
	i = min(max(i+delta, 0), len(m.win)-1)
	m.selected = m.win[i].ID
	m.ensureVisible(i)
}

// ensureVisible scrolls the least amount that shows message i whole, or
// its top if it is taller than the viewport.
func (m *Model) ensureVisible(i int) {
	a := m.idx[m.anchor]
	top, cut, _ := m.topVisible()
	switch {
	case i > a || (i == a && m.skip > 0):
		m.follow = false
		m.anchor, m.skip = m.win[i].ID, 0
		if m.newerDone && m.atBottom() {
			m.follow, m.unseen = true, 0
		}
		m.clampAnchor()
	case i < top || (i == top && cut > 0) || m.height(i) > m.h:
		m.showFromTop(i)
	}
}

// showFromTop puts the first line of message i at the top of the
// viewport, as far as the content below allows.
func (m *Model) showFromTop(i int) {
	m.follow = false
	total := 0
	for j := i; j < len(m.win); j++ {
		total += m.height(j)
		if total >= m.h {
			m.anchor, m.skip = m.win[j].ID, total-m.h
			m.clampAnchor()
			return
		}
	}
	m.anchor, m.skip = m.win[len(m.win)-1].ID, 0
	if m.newerDone {
		m.follow, m.unseen = true, 0
	}
	m.clampAnchor()
}

// selectVisible moves the selection into the viewport after a scroll:
// to the bottom-most visible message when scrolling down, to the top-most
// when scrolling up. A selection already on screen stays.
func (m *Model) selectVisible(down bool) {
	top, cut, _ := m.topVisible()
	a := m.idx[m.anchor]
	if i, ok := m.idx[m.selected]; ok && i >= top && i <= a {
		if (i != top || cut == 0) && (i != a || m.skip == 0) {
			return
		}
	}
	switch {
	case down:
		if m.skip > 0 && a > top {
			a--
		}
		m.selected = m.win[a].ID
	case cut > 0 && top < a:
		m.selected = m.win[top+1].ID
	default:
		m.selected = m.win[top].ID
	}
}
