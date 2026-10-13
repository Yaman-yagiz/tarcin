// SPDX-License-Identifier: GPL-3.0-or-later

// Package clipboard puts text on the system clipboard: through wl-copy on
// Wayland, and xclip or xsel on X11, when one is installed; otherwise
// through the terminal (OSC 52), which works over SSH but which some
// terminals refuse, VTE's (GNOME Console, GNOME Terminal) among them.
package clipboard

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ResultMsg reports how text was copied: Tool is the program that did
// it, or "" when the terminal was asked to, which it may ignore.
type ResultMsg struct {
	Tool string
}

// tools are the clipboard programs tried in order, each with the
// display variable that shows its display server is there.
var tools = []struct {
	env  string
	argv []string
}{
	{"WAYLAND_DISPLAY", []string{"wl-copy"}},
	{"DISPLAY", []string{"xclip", "-selection", "clipboard", "-in"}},
	{"DISPLAY", []string{"xsel", "--clipboard", "--input"}},
}

// Seams for tests.
var (
	getenv   = os.Getenv
	lookPath = exec.LookPath
	run      = runTool
)

// runTool gives text to a clipboard program on its standard input. The
// program forks to keep serving the clipboard and its parent exits; its
// output goes nowhere, so nothing waits on the fork and the terminal is
// left alone. No shell runs.
func runTool(argv []string, text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // a fixed clipboard program found in $PATH
	c.Stdin = strings.NewReader(text)
	return c.Run()
}

// Copy returns the command that copies text and then reports a
// ResultMsg. It runs off the UI goroutine.
func Copy(text string) tea.Cmd {
	return func() tea.Msg {
		for _, t := range tools {
			if getenv(t.env) == "" {
				continue
			}
			path, err := lookPath(t.argv[0])
			if err != nil {
				continue
			}
			if run(append([]string{path}, t.argv[1:]...), text) == nil {
				return ResultMsg{Tool: t.argv[0]}
			}
		}
		return tea.BatchMsg{tea.SetClipboard(text), func() tea.Msg { return ResultMsg{} }}
	}
}
