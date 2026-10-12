// SPDX-License-Identifier: GPL-3.0-or-later

package ui

// Layout limits, in terminal cells.
const (
	// NarrowWidth is the width below which only one pane is shown at a time.
	NarrowWidth = 80
	// MinWidth and MinHeight are the smallest usable terminal size; below it
	// the UI shows a "terminal too small" notice instead of panes.
	MinWidth  = 20
	MinHeight = 5

	listMinWidth = 24
	listMaxWidth = 40
	// MaxComposerHeight caps the composer so the history stays visible.
	MaxComposerHeight = 6
)

// Rect is a rectangle of terminal cells. The zero Rect is empty.
type Rect struct {
	X, Y, W, H int
}

// Empty reports whether r covers no cells.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Layout is the geometry of one frame. Regions that are not shown are
// empty. In the wide layout a one-column separator sits between the chat
// list and the chat, and a one-row separator above the composer.
type Layout struct {
	Width, Height int
	TooSmall      bool // below MinWidth or MinHeight
	Narrow        bool // one pane at a time
	ShowList      bool
	ShowChat      bool

	List     Rect
	Header   Rect // chat title line
	Messages Rect
	Composer Rect
	Status   Rect
}

// computeLayout places the regions for a width×height terminal. In narrow
// mode listScreen chooses between the chat list and the chat.
// composerHeight is clamped to [1, MaxComposerHeight] and to the space
// available.
func computeLayout(width, height, composerHeight int, listScreen bool) Layout {
	l := Layout{Width: max(width, 0), Height: max(height, 0)}
	if width < MinWidth || height < MinHeight {
		l.TooSmall = true
		return l
	}
	l.Narrow = width < NarrowWidth
	l.Status = Rect{X: 0, Y: height - 1, W: width, H: 1}
	body := height - 1

	chatX, chatW := 0, width
	switch {
	case l.Narrow && listScreen:
		l.ShowList = true
		l.List = Rect{X: 0, Y: 0, W: width, H: body}
		return l
	case !l.Narrow:
		l.ShowList = true
		listW := min(max(width*3/10, listMinWidth), listMaxWidth)
		l.List = Rect{X: 0, Y: 0, W: listW, H: body}
		chatX, chatW = listW+1, width-listW-1
	}

	l.ShowChat = true
	// Header (1) + messages (≥1) + separator (1) + composer (≥1).
	composerHeight = min(max(composerHeight, 1), MaxComposerHeight, body-3)
	l.Header = Rect{X: chatX, Y: 0, W: chatW, H: 1}
	l.Composer = Rect{X: chatX, Y: body - composerHeight, W: chatW, H: composerHeight}
	l.Messages = Rect{X: chatX, Y: 1, W: chatW, H: body - 2 - composerHeight}
	return l
}
