// SPDX-License-Identifier: GPL-3.0-or-later

// Package theme defines the styles the UI renders with. Styles are built
// once per theme, never per frame (docs/architecture/ui.md §3.3).
package theme

import "charm.land/lipgloss/v2"

// Theme is a complete set of UI styles. ID is part of the render-cache
// key, so two themes must never share an ID.
type Theme struct {
	ID string

	Separator     lipgloss.Style // pane borders
	Header        lipgloss.Style // chat header, unfocused
	HeaderFocused lipgloss.Style // chat header while the chat has focus
	Status        lipgloss.Style // status line background
	StatusMode    lipgloss.Style // the mode indicator in the status line
	Muted         lipgloss.Style // secondary text: timestamps, hints
	Accent        lipgloss.Style // focus and highlights
	Warning       lipgloss.Style // problems the user should notice

	Selected        lipgloss.Style // selected row in an unfocused list
	SelectedFocused lipgloss.Style // selected row in the focused list
	Unread          lipgloss.Style // titles of chats with unread messages
	Badge           lipgloss.Style // unread counters

	// Senders colors sender names; a sender always gets the same style.
	Senders   []lipgloss.Style
	Gutter    lipgloss.Style // the selection bar beside a selected message
	Day       lipgloss.Style // day separators in the message view
	Quote     lipgloss.Style // quoted replies
	Indicator lipgloss.Style // "new messages below"
}

// Sender returns the style for a sender ID.
func (t *Theme) Sender(id int64) lipgloss.Style {
	if len(t.Senders) == 0 {
		return lipgloss.NewStyle().Bold(true)
	}
	if id < 0 {
		id = -id
	}
	return t.Senders[id%int64(len(t.Senders))]
}

// Default uses the terminal's 16-color ANSI palette, so it follows the
// user's terminal theme on both light and dark backgrounds.
func Default() *Theme {
	return &Theme{
		ID:            "default",
		Separator:     lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Header:        lipgloss.NewStyle().Bold(true),
		HeaderFocused: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		Status:        lipgloss.NewStyle().Reverse(true),
		StatusMode:    lipgloss.NewStyle().Reverse(true).Bold(true),
		Muted:         lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Accent:        lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		Warning:       lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true),

		Selected:        lipgloss.NewStyle().Reverse(true).Faint(true),
		SelectedFocused: lipgloss.NewStyle().Reverse(true),
		Unread:          lipgloss.NewStyle().Bold(true),
		Badge:           lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true),

		Senders:   senderStyles(),
		Gutter:    lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		Day:       lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Quote:     lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true),
		Indicator: lipgloss.NewStyle().Reverse(true).Bold(true),
	}
}

// senderStyles uses the six chromatic ANSI colors, each readable on
// light and dark backgrounds.
func senderStyles() []lipgloss.Style {
	var s []lipgloss.Style
	for _, c := range []string{"1", "2", "3", "4", "5", "6"} {
		s = append(s, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(c)))
	}
	return s
}

// Plain has no colors, only attributes that survive monochrome terminals.
// Information never depends on color alone (ui.md §9), so Plain loses
// nothing but decoration.
func Plain() *Theme {
	return &Theme{
		ID:            "plain",
		Header:        lipgloss.NewStyle().Bold(true),
		HeaderFocused: lipgloss.NewStyle().Bold(true).Underline(true),
		Status:        lipgloss.NewStyle().Reverse(true),
		StatusMode:    lipgloss.NewStyle().Reverse(true).Bold(true),
		Accent:        lipgloss.NewStyle().Bold(true),
		Warning:       lipgloss.NewStyle().Bold(true),

		Selected:        lipgloss.NewStyle().Underline(true),
		SelectedFocused: lipgloss.NewStyle().Reverse(true),
		Unread:          lipgloss.NewStyle().Bold(true),
		Badge:           lipgloss.NewStyle().Bold(true),

		Gutter:    lipgloss.NewStyle().Bold(true),
		Quote:     lipgloss.NewStyle().Italic(true),
		Indicator: lipgloss.NewStyle().Reverse(true),
	}
}

// HighContrast has no colors and no faint text, whose contrast depends
// on the terminal's palette; it marks everything with bold, underline
// and reverse video, which keep the terminal's own foreground and
// background at full contrast on light and dark themes. Selections and
// warnings are always reversed.
func HighContrast() *Theme {
	return &Theme{
		ID:            "high-contrast",
		Header:        lipgloss.NewStyle().Bold(true),
		HeaderFocused: lipgloss.NewStyle().Bold(true).Reverse(true),
		Status:        lipgloss.NewStyle().Reverse(true),
		StatusMode:    lipgloss.NewStyle().Reverse(true).Bold(true).Underline(true),
		Accent:        lipgloss.NewStyle().Bold(true).Underline(true),
		Warning:       lipgloss.NewStyle().Bold(true).Reverse(true),

		Selected:        lipgloss.NewStyle().Reverse(true),
		SelectedFocused: lipgloss.NewStyle().Reverse(true).Bold(true),
		Unread:          lipgloss.NewStyle().Bold(true).Underline(true),
		Badge:           lipgloss.NewStyle().Bold(true).Reverse(true),

		Senders:   []lipgloss.Style{lipgloss.NewStyle().Bold(true)},
		Gutter:    lipgloss.NewStyle().Bold(true),
		Day:       lipgloss.NewStyle().Bold(true),
		Quote:     lipgloss.NewStyle(),
		Indicator: lipgloss.NewStyle().Reverse(true).Bold(true),
	}
}
