// SPDX-License-Identifier: GPL-3.0-or-later

package keymap

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// dumpHistory is how many keys the dump screen keeps.
const dumpHistory = 20

// DumpModel shows how tarcin names each key the terminal sends, so users
// can write bindings for their terminal (`tarcin --dump-keys`). Pressing
// ctrl+c twice in a row quits; every other key is shown, including ctrl+c.
type DumpModel struct {
	lines    []string
	lastCtrl bool
}

// NewDumpModel returns the key-dump screen.
func NewDumpModel() *DumpModel { return &DumpModel{} }

// Init implements tea.Model.
func (d *DumpModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (d *DumpModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		name := msg.String()
		if name == "ctrl+c" && d.lastCtrl {
			return d, tea.Quit
		}
		d.lastCtrl = name == "ctrl+c"
		d.lines = append(d.lines, describe(msg))
		if len(d.lines) > dumpHistory {
			d.lines = d.lines[len(d.lines)-dumpHistory:]
		}
	case tea.PasteMsg:
		d.lines = append(d.lines, fmt.Sprintf("paste of %d bytes (bracketed paste works)", len(msg.Content)))
	}
	return d, nil
}

func describe(msg tea.KeyPressMsg) string {
	k := msg.Key()
	text := ""
	if k.Text != "" {
		text = fmt.Sprintf("  text=%q", k.Text)
	}
	extra := ""
	if k.ShiftedCode != 0 || k.BaseCode != 0 {
		extra = fmt.Sprintf("  shifted=%U base=%U", k.ShiftedCode, k.BaseCode)
	}
	return fmt.Sprintf("%-18q code=%U mod=%d%s%s", msg.String(), k.Code, k.Mod, text, extra)
}

// View implements tea.Model.
func (d *DumpModel) View() tea.View {
	var b strings.Builder
	b.WriteString("tarcin key dump: press keys to see the names to use in config.toml.\n")
	b.WriteString("Press ctrl+c twice to quit.\n\n")
	for _, l := range d.lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}
