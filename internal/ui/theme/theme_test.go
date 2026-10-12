// SPDX-License-Identifier: GPL-3.0-or-later

package theme

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestThemes(t *testing.T) {
	d, p := Default(), Plain()
	if d.ID == "" || p.ID == "" || d.ID == p.ID {
		t.Fatalf("theme IDs must be unique and non-empty: %q %q", d.ID, p.ID)
	}
	if _, ok := p.Separator.GetForeground().(lipgloss.NoColor); !ok {
		t.Error("Plain must not set colors")
	}
	if _, ok := d.Separator.GetForeground().(lipgloss.NoColor); ok {
		t.Error("Default must color separators")
	}
	if !p.HeaderFocused.GetBold() || !p.HeaderFocused.GetUnderline() {
		t.Error("Plain must mark focus without color")
	}
	if d.Sender(1).GetForeground() != d.Sender(7).GetForeground() || d.Sender(1).GetForeground() == d.Sender(2).GetForeground() {
		t.Error("sender styles must be stable per ID and differ between neighbors")
	}
	if !p.Sender(-3).GetBold() {
		t.Error("Plain senders must be bold")
	}
	h := HighContrast()
	if h.ID == d.ID || h.ID == p.ID {
		t.Fatalf("theme ID %q is taken", h.ID)
	}
	for name, s := range map[string]lipgloss.Style{"Muted": h.Muted, "Selected": h.Selected, "Separator": h.Separator, "Quote": h.Quote, "Day": h.Day} {
		if s.GetFaint() {
			t.Errorf("HighContrast %s is faint", name)
		}
		if _, ok := s.GetForeground().(lipgloss.NoColor); !ok {
			t.Errorf("HighContrast %s has a color", name)
		}
	}
	if !h.Selected.GetReverse() || !h.SelectedFocused.GetReverse() || !h.Warning.GetReverse() {
		t.Error("HighContrast reverses selections and warnings")
	}
}
