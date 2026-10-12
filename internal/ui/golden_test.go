// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/devtools/gen"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
)

// Golden snapshots of the main screens. After an intended change to the
// UI, review the diff and rewrite them with:
//
//	go test ./internal/ui -run TestGolden -update
var update = flag.Bool("update", false, "rewrite the golden screen snapshots")

// goldenModel builds a model over a small generated account with a fixed
// clock, so screens are identical on every run.
func goldenModel(t *testing.T, w, h int) *Model {
	t.Helper()
	return goldenModelWith(t, w, h, nil)
}

// goldenModelWith is goldenModel with a theme; nil is the default.
func goldenModelWith(t *testing.T, w, h int, th *theme.Theme) *Model {
	t.Helper()
	d := gen.Build(gen.Spec{Seed: 21, Chats: 12, Messages: 600, HotChatMessages: 300, GroupRatio: 0.5, MembersPerGroup: 6})
	hist := genHistory{}
	for i := range d.Chats {
		hist[d.Chats[i].ID] = slices.Collect(d.Messages(i))
	}
	names := msglist.NamesFunc(func(id domain.IdentityID) string { return d.Identities[id-1].DisplayName() })
	m := New(Options{
		Theme:    th,
		History:  hist,
		Names:    names,
		Clock:    func() time.Time { return gen.DefaultEnd.Add(time.Hour) },
		Location: time.UTC,
	})
	size(m, w, h)
	rows := make([]chatlist.Row, 0, len(d.Chats))
	for i := range d.Chats {
		c := &d.Chats[i]
		title := c.Title
		if c.Kind == domain.ChatDirect {
			title = d.Identities[c.PeerID-1].DisplayName()
		}
		rows = append(rows, chatlist.Row{ID: c.ID, Title: title, LastActivity: c.LastActivity, Unread: c.UnreadCount, PinnedAt: c.PinnedAt, Muted: c.IsMuted(gen.DefaultEnd), Archived: c.Archived})
	}
	m.Update(chatlist.LoadMsg{Rows: rows})
	m.Update(BatchMsg{Connection: &domain.ConnectionChanged{State: domain.ConnOnline}})
	return m
}

func press(m *Model, keys ...tea.KeyPressMsg) {
	for _, k := range keys {
		_, cmd := m.Update(k)
		run(m, cmd)
	}
}

func text(s string) []tea.KeyPressMsg {
	var out []tea.KeyPressMsg
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

func screen(m *Model) string { return ansi.Strip(m.View().Content) + "\n" }

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // test data directory
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil { //nolint:gosec // test data, not secret
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed test data path
	if err != nil {
		t.Fatalf("missing golden file (run with -update): %v", err)
	}
	if got == string(want) {
		return
	}
	g, w := strings.Split(got, "\n"), strings.Split(string(want), "\n")
	for i := range max(len(g), len(w)) {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			t.Fatalf("%s differs at line %d:\n got: %q\nwant: %q\n(review and run with -update if the change is intended)", name, i+1, gl, wl)
		}
	}
}

func TestGolden(t *testing.T) {
	type step func(*Model)
	enter := tea.KeyPressMsg{Code: tea.KeyEnter}
	screens := []struct {
		name  string
		steps []step
	}{
		{"chats", nil},
		{"chat", []step{func(m *Model) { press(m, enter) }}},
		{"compose", []step{func(m *Model) {
			press(m, enter)
			press(m, text("i")...)
			press(m, text("Merhaba, akşam görüşelim mi?")...)
		}}},
		{"help", []step{func(m *Model) { press(m, text("?")...) }}},
		{"search", []step{func(m *Model) { press(m, text("/a")...) }}},
		{"command", []step{func(m *Model) { press(m, text(":open ")...) }}},
	}
	for _, sz := range [][2]int{{80, 24}, {120, 40}} {
		for _, s := range screens {
			name := fmt.Sprintf("%s-%dx%d", s.name, sz[0], sz[1])
			t.Run(name, func(t *testing.T) {
				m := goldenModel(t, sz[0], sz[1])
				for _, st := range s.steps {
					st(m)
				}
				checkGolden(t, name, screen(m))
			})
		}
	}
	t.Run("start-80x24", func(t *testing.T) {
		m := New(Options{History: genHistory{}, Clock: func() time.Time { return gen.DefaultEnd }, Location: time.UTC})
		size(m, 80, 24)
		checkGolden(t, "start-80x24", screen(m))
	})
	t.Run("chat-narrow-60x20", func(t *testing.T) {
		m := goldenModel(t, 60, 20)
		press(m, enter)
		checkGolden(t, "chat-narrow-60x20", screen(m))
	})
}
