// SPDX-License-Identifier: GPL-3.0-or-later

package uikit

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
)

func TestFit(t *testing.T) {
	for _, tt := range []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 3, "abc"},
		{"田中", 3, "田 "}, // a wide character that does not fit leaves a gap
		{"", 2, "  "},
	} {
		if got := Fit(ansi.WcWidth, tt.in, tt.w); got != tt.want {
			t.Errorf("fit(%q, %d) = %q, want %q", tt.in, tt.w, got, tt.want)
		}
	}
	if got := FitLines(ansi.WcWidth, "a\nb\nc", 1, 2); len(got) != 2 || got[1] != "b" {
		t.Errorf("fitLines must cut extra lines: %q", got)
	}
}

func TestFitLinesCounts(t *testing.T) {
	if got := FitLines(ansi.WcWidth, "", 3, 0); len(got) != 0 {
		t.Errorf("h=0 gave %d lines", len(got))
	}
	if got := FitLines(ansi.WcWidth, "abc", 0, 2); len(got) != 2 || got[0] != "" {
		t.Errorf("w=0 must give blank lines: %q", got)
	}
}

func TestNewContext(t *testing.T) {
	c := NewContext(theme.Default())
	if c.Width != ansi.WcWidth || c.Theme.ID != "default" {
		t.Fatalf("NewContext = %+v", c)
	}
}
