// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"context"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/devtools/gen"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
)

// genHistory serves a generated dataset.
type genHistory map[domain.ChatID][]domain.Message

func (h genHistory) Latest(_ context.Context, chat domain.ChatID, limit int) ([]domain.Message, error) {
	msgs := h[chat]
	return slices.Clone(msgs[max(len(msgs)-limit, 0):]), nil
}

func (h genHistory) Before(_ context.Context, chat domain.ChatID, c domain.Cursor, limit int) ([]domain.Message, error) {
	msgs := h[chat]
	i, _ := slices.BinarySearchFunc(msgs, c, func(m domain.Message, c domain.Cursor) int {
		if m.Cursor().Before(c) {
			return -1
		}
		return 1
	})
	return slices.Clone(msgs[max(i-limit, 0):i]), nil
}

func (h genHistory) After(_ context.Context, chat domain.ChatID, c domain.Cursor, limit int) ([]domain.Message, error) {
	msgs := h[chat]
	i, _ := slices.BinarySearchFunc(msgs, c, func(m domain.Message, c domain.Cursor) int {
		if c.Before(m.Cursor()) {
			return 1
		}
		return -1
	})
	return slices.Clone(msgs[i:min(i+limit, len(msgs))]), nil
}

// run executes cmd and everything it leads to, synchronously.
func run(m *Model, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
}

// busyModel is a 200x50 screen over 2,000 chats whose first chat, a
// group, holds n messages and is open.
func busyModel(tb testing.TB, n int) (*Model, *gen.Dataset) {
	tb.Helper()
	d := gen.Build(gen.Spec{Seed: 8, Chats: 2000, Messages: n + 6000, HotChatMessages: n, GroupRatio: 0.3, MembersPerGroup: 20})
	hist := genHistory{}
	for i := range d.Chats {
		hist[d.Chats[i].ID] = slices.Collect(d.Messages(i))
	}
	names := msglist.NamesFunc(func(id domain.IdentityID) string { return d.Identities[id-1].DisplayName() })
	m := New(Options{History: hist, Names: names})
	size(m, 200, 50)
	rows := make([]chatlist.Row, 0, len(d.Chats))
	for i := range d.Chats {
		c := &d.Chats[i]
		title := c.Title
		if c.Kind == domain.ChatDirect {
			title = d.Identities[c.PeerID-1].DisplayName()
		}
		rows = append(rows, chatlist.Row{ID: c.ID, Title: title, LastActivity: c.LastActivity, Unread: c.UnreadCount, PinnedAt: c.PinnedAt})
	}
	m.Update(chatlist.LoadMsg{Rows: rows})
	run(m, m.open(1))
	m.View()
	return m, d
}

var frameSizes = []struct {
	name string
	n    int
}{{"10k", 10_000}, {"100k", 100_000}}

// BenchmarkKeypressFrame is budget B3: a key press in the message view
// and the frame that follows.
func BenchmarkKeypressFrame(b *testing.B) {
	for _, sz := range frameSizes {
		b.Run(sz.name, func(b *testing.B) {
			m, _ := busyModel(b, sz.n)
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				k := key("k")
				if i/30%2 == 1 {
					k = key("j")
				}
				i++
				_, cmd := m.Update(k)
				run(m, cmd)
				_ = m.View()
			}
		})
	}
}

// BenchmarkBurstFrame is the per-frame cost behind budget B4: one bridge
// batch of 20 new messages in the open chat and 20 chat list updates.
func BenchmarkBurstFrame(b *testing.B) {
	m, d := busyModel(b, 100_000)
	last := d.Chats[0].LastActivity
	next := domain.MessageID(10_000_000)
	b.ReportAllocs()
	for b.Loop() {
		batch := BatchMsg{Appends: map[domain.ChatID][]domain.Message{}}
		for k := range 20 {
			next++
			last = last.Add(time.Second)
			batch.Appends[1] = append(batch.Appends[1], domain.Message{
				ID: next, ChatID: 1, SenderID: 2, Timestamp: last, Kind: domain.KindText, Body: "canlı mesaj geldi 👍",
			})
			batch.Rows = append(batch.Rows, chatlist.Row{ID: domain.ChatID(2 + k), Title: "Grup", LastActivity: last, Unread: k})
		}
		_, cmd := m.Update(batch)
		run(m, cmd)
		_ = m.View()
	}
}

// BenchmarkChatListFrame moves through the chat list and renders.
func BenchmarkChatListFrame(b *testing.B) {
	m, _ := busyModel(b, 10_000)
	m.setFocus(FocusChatList)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		k := key("j")
		if i/200%2 == 1 {
			k = key("k")
		}
		i++
		m.Update(k)
		_ = m.View()
	}
}

// TestFrameAllocationsDoNotGrowWithHistory: a key press and frame cost
// the same allocations with 10k and 100k messages in the open chat.
func TestFrameAllocationsDoNotGrowWithHistory(t *testing.T) {
	allocs := func(n int) float64 {
		m, _ := busyModel(t, n)
		for range 50 {
			m.Update(key("k"))
		}
		m.View()
		return testing.AllocsPerRun(50, func() {
			m.Update(key("k"))
			m.Update(key("j"))
			_ = m.View()
		})
	}
	small, large := allocs(10_000), allocs(100_000)
	if large > small*1.2+4 {
		t.Fatalf("a frame allocates %.0f times with 100k messages vs %.0f with 10k", large, small)
	}
}
