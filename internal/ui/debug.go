// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// MetricSection is a titled group of counters in the debug pane.
type MetricSection struct {
	Title string
	Items [][2]string // name, value
}

// MetricsMsg delivers the app's counters, from Options.Metrics.
type MetricsMsg struct{ Sections []MetricSection }

// debugTickMsg refreshes the debug pane while it shows.
type debugTickMsg struct{}

// debugEvery is how often the debug pane refreshes.
const debugEvery = time.Second

// debugCommand runs :debug, which shows or hides the debug pane: the
// counters of the store, the jobs, the bridge, the process and the UI.
func (m *Model) debugCommand() tea.Cmd {
	if m.mode == modeDebug {
		m.mode = modeNormal
		return nil
	}
	m.mode = modeDebug
	m.keys.Reset()
	return m.refreshDebug()
}

func (m *Model) refreshDebug() tea.Cmd {
	tick := tea.Tick(debugEvery, func(time.Time) tea.Msg { return debugTickMsg{} })
	if m.metricsFn == nil {
		return tick
	}
	return tea.Batch(m.metricsFn(), tick)
}

// debugKey closes the pane on esc, q or :debug's key; quit quits.
func (m *Model) debugKey(msg tea.KeyPressMsg) tea.Cmd {
	switch k := msg.String(); {
	case slices.Contains(m.quitKeys, k):
		return tea.Quit
	case k == "esc" || k == "q":
		m.mode = modeNormal
	}
	return nil
}

// debugView renders the app's sections and the UI's own counters.
func (m *Model) debugView() string {
	t := m.ctx.Theme
	lines := []string{t.Header.Render("tarcin debug") + t.Muted.Render(" · refreshes every second · esc closes")}
	section := func(s MetricSection) {
		lines = append(lines, "", t.Accent.Render(s.Title))
		for _, it := range s.Items {
			lines = append(lines, fmt.Sprintf("  %-24s %s", it[0], it[1]))
		}
	}
	for _, s := range m.metrics {
		section(s)
	}
	lookups, misses, window := m.msgs.CacheStats()
	rowLookups, rowMisses, rows := m.chats.CacheStats()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	section(MetricSection{Title: "UI", Items: [][2]string{
		{"frames", strconv.Itoa(m.frames)},
		{"message cache", hitRate(lookups, misses) + " of " + strconv.Itoa(lookups) + " lookups"},
		{"messages in window", strconv.Itoa(window)},
		{"chat row cache", hitRate(rowLookups, rowMisses) + " of " + strconv.Itoa(rowLookups) + " lookups"},
		{"chats", strconv.Itoa(rows)},
	}})
	section(MetricSection{Title: "Process", Items: [][2]string{
		{"goroutines", strconv.Itoa(runtime.NumGoroutine())},
		{"heap", fmt.Sprintf("%.1f MiB", float64(mem.HeapAlloc)/(1<<20))},
		{"garbage collections", strconv.FormatUint(uint64(mem.NumGC), 10)},
	}})
	return strings.Join(lines, "\n")
}

func hitRate(lookups, misses int) string {
	if lookups == 0 {
		return "–"
	}
	return fmt.Sprintf("%.1f%% hits", 100*float64(lookups-misses)/float64(lookups))
}
