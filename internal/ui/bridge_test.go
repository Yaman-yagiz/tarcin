// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/linkscreen"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
)

// sink records batches delivered by a bridge.
type sink struct {
	mu      sync.Mutex
	batches []BatchMsg
	got     chan struct{}
	delay   time.Duration
	inSend  atomic.Int32
	overlap atomic.Bool
}

func newSink() *sink { return &sink{got: make(chan struct{}, 1000)} }

func (s *sink) send(msg tea.Msg) {
	if s.inSend.Add(1) > 1 {
		s.overlap.Store(true)
	}
	defer s.inSend.Add(-1)
	time.Sleep(s.delay)
	s.mu.Lock()
	s.batches = append(s.batches, msg.(BatchMsg))
	s.mu.Unlock()
	s.got <- struct{}{}
}

// wait blocks until the next batch arrives and returns all batches.
func (s *sink) wait(t *testing.T) []BatchMsg {
	t.Helper()
	select {
	case <-s.got:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a batch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.batches)
}

func msgs(chat domain.ChatID, from, to int) []domain.Message {
	var out []domain.Message
	for i := from; i < to; i++ {
		out = append(out, domain.Message{ID: domain.MessageID(i), ChatID: chat})
	}
	return out
}

func TestBridgeCoalesces(t *testing.T) {
	s := newSink()
	b := NewBridge(s.send, BridgeOptions{FlushInterval: 50 * time.Millisecond})
	defer b.Close()
	for i := range 1000 {
		chat := domain.ChatID(1 + i%10)
		b.Messages(chat, msgs(chat, i, i+1))
		b.Chat(chatlist.Row{ID: chat, Unread: i})
	}
	batches := s.wait(t)
	if len(batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(batches))
	}
	got := batches[0]
	if len(got.Rows) != 10 || len(got.Appends) != 10 {
		t.Fatalf("rows %d, chats with messages %d", len(got.Rows), len(got.Appends))
	}
	for chat, list := range got.Appends {
		if len(list) != 100 || !slices.IsSortedFunc(list, func(a, b domain.Message) int { return int(a.ID - b.ID) }) {
			t.Fatalf("chat %d: %d messages, or out of order", chat, len(list))
		}
	}
	for _, r := range got.Rows {
		if r.Unread < 990 {
			t.Fatalf("row %d kept a stale state (unread %d)", r.ID, r.Unread)
		}
	}
	if f, o := b.Stats(); f != 1 || o != 0 {
		t.Fatalf("stats = %d flushes, %d overflows", f, o)
	}
}

func TestBridgeOverflowBecomesReload(t *testing.T) {
	s := newSink()
	b := NewBridge(s.send, BridgeOptions{FlushInterval: 30 * time.Millisecond, ChatCap: 50})
	defer b.Close()
	b.Messages(1, msgs(1, 0, 40))
	b.Updates(1, msgs(1, 0, 20)) // 60 > 50: chat 1 overflows
	b.Messages(1, msgs(1, 40, 45))
	b.Messages(2, msgs(2, 0, 10))
	got := s.wait(t)[0]
	if !slices.Equal(got.Reloads, []domain.ChatID{1}) {
		t.Fatalf("reloads = %v, want [1]", got.Reloads)
	}
	if _, ok := got.Appends[1]; ok || got.Replaces != nil {
		t.Fatal("an overflowed chat must carry no message details")
	}
	if len(got.Appends[2]) != 10 {
		t.Fatal("other chats must be unaffected")
	}
	if _, o := b.Stats(); o != 1 {
		t.Fatalf("overflows = %d", o)
	}
}

func TestBridgeLatestStateWins(t *testing.T) {
	s := newSink()
	b := NewBridge(s.send, BridgeOptions{FlushInterval: 30 * time.Millisecond})
	defer b.Close()
	b.Typing(3, true)
	b.Typing(3, false)
	b.Connection(domain.ConnectionChanged{State: domain.ConnConnecting})
	b.Connection(domain.ConnectionChanged{State: domain.ConnOnline})
	got := s.wait(t)[0]
	if got.Typing[3] || got.Connection == nil || got.Connection.State != domain.ConnOnline {
		t.Fatalf("batch = %+v", got)
	}
}

func TestBridgeIsIdleWithoutEvents(t *testing.T) {
	s := newSink()
	b := NewBridge(s.send, BridgeOptions{FlushInterval: 10 * time.Millisecond})
	defer b.Close()
	b.Typing(1, true)
	s.wait(t)
	time.Sleep(100 * time.Millisecond)
	if f, _ := b.Stats(); f != 1 {
		t.Fatalf("an idle bridge sent %d batches", f)
	}
	b.Messages(1, nil) // nothing to send
	time.Sleep(50 * time.Millisecond)
	if f, _ := b.Stats(); f != 1 {
		t.Fatal("an empty message list must not wake the bridge")
	}
}

// TestBridgeSerializesSends uses a slow consumer: batches never overlap,
// arrive in order, and nothing is lost while the UI is busy.
func TestBridgeSerializesSends(t *testing.T) {
	s := newSink()
	s.delay = 20 * time.Millisecond
	b := NewBridge(s.send, BridgeOptions{FlushInterval: 5 * time.Millisecond, ChatCap: 1 << 20})
	var wg sync.WaitGroup
	for p := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				id := p*1000 + i
				b.Messages(1, msgs(1, id, id+1))
				if i%50 == 0 {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
	deadline := time.Now().Add(5 * time.Second)
	var total int
	for time.Now().Before(deadline) {
		s.mu.Lock()
		total = 0
		for _, batch := range s.batches {
			total += len(batch.Appends[1])
		}
		s.mu.Unlock()
		if total == 2000 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.Close()
	if total != 2000 {
		t.Fatalf("delivered %d of 2000 messages", total)
	}
	if s.overlap.Load() {
		t.Fatal("two sends ran at the same time")
	}
	// Each producer's messages arrive in its own order.
	last := map[int]int{}
	for _, batch := range s.batches {
		for _, m := range batch.Appends[1] {
			p, i := int(m.ID)/1000, int(m.ID)%1000
			if prev, ok := last[p]; ok && i <= prev {
				t.Fatalf("producer %d: message %d after %d", p, i, prev)
			}
			last[p] = i
		}
	}
}

func TestBridgeClose(t *testing.T) {
	s := newSink()
	b := NewBridge(s.send, BridgeOptions{FlushInterval: time.Hour})
	b.Typing(1, true)
	b.Close()
	b.Close() // idempotent
	b.Messages(1, msgs(1, 0, 1))
	b.Updates(1, msgs(1, 0, 1))
	b.Chat(chatlist.Row{ID: 1})
	b.Typing(1, true)
	b.Connection(domain.ConnectionChanged{})
	if f, _ := b.Stats(); f != 0 {
		t.Fatal("a closed bridge must not send")
	}
}

func TestRootAppliesBatches(t *testing.T) {
	m := New(Options{History: emptyHistory{}})
	size(m, 100, 30)
	now := time.Now()
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 1, Title: "Aile", LastActivity: now}}})
	_, open := m.Update(key("enter"))
	_, load := m.Update(open())
	m.Update(load())

	live := domain.Message{ID: 10, ChatID: 1, Timestamp: now, Kind: domain.KindText, Body: "canlı mesaj", SenderID: 2}
	m.Update(BatchMsg{
		Rows:       []chatlist.Row{{ID: 2, Title: "Yeni sohbet", LastActivity: now.Add(time.Second), Unread: 1}},
		Appends:    map[domain.ChatID][]domain.Message{1: {live}, 2: {{ID: 11, ChatID: 2}}},
		Typing:     map[domain.ChatID]bool{2: true},
		Connection: &domain.ConnectionChanged{State: domain.ConnOnline},
	})
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Yeni sohbet", "canlı mesaj", "\u2026"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if m.conn.State != domain.ConnOnline {
		t.Error("connection state not applied")
	}
	_, cmd := m.Update(BatchMsg{Reloads: []domain.ChatID{1}})
	if cmd == nil {
		t.Fatal("a reload of the open chat must reload it")
	}
	if _, cmd := m.Update(BatchMsg{Reloads: []domain.ChatID{7}}); cmd != nil {
		t.Fatal("reloads of other chats must be ignored")
	}
}

func TestBridgeReloadRemoveNamesAndNotices(t *testing.T) {
	s := newSink()
	b := NewBridge(s.send, BridgeOptions{FlushInterval: 5 * time.Millisecond})
	defer b.Close()
	b.Messages(1, msgs(1, 0, 3))
	b.Reload(1)
	b.Messages(1, msgs(1, 3, 4)) // a reload makes later details pointless
	b.Chat(chatlist.Row{ID: 2, Title: "gone"})
	b.Typing(2, true)
	b.RemoveChat(2)
	b.Chat(chatlist.Row{ID: 2, Title: "late"})
	b.Messages(2, msgs(2, 0, 1))
	b.Reload(2)
	b.NamesChanged()
	b.Notice("")
	b.Notice("disk full")
	got := s.wait(t)
	if len(got) != 1 {
		t.Fatalf("%d batches", len(got))
	}
	batch := got[0]
	if !slices.Equal(batch.Reloads, []domain.ChatID{1}) || len(batch.Appends) != 0 {
		t.Errorf("reloads %v, appends %v", batch.Reloads, batch.Appends)
	}
	if !slices.Equal(batch.Removed, []domain.ChatID{2}) || len(batch.Rows) != 0 || len(batch.Typing) != 0 {
		t.Errorf("a removed chat must carry nothing else: %+v", batch)
	}
	if !batch.NamesChanged || batch.Notice != "disk full" {
		t.Errorf("names %v, notice %q", batch.NamesChanged, batch.Notice)
	}
}

func TestRootHandlesRemovedChatsAndNames(t *testing.T) {
	m := New(Options{History: emptyHistory{}})
	size(m, 100, 30)
	now := time.Now()
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 1, Title: "Ali (number)", LastActivity: now}, {ID: 2, Title: "Ali", LastActivity: now}}})
	m.Update(key("j"))
	_, open := m.Update(key("enter"))
	_, load := m.Update(open())
	m.Update(load())
	if m.msgs.Chat() == 0 {
		t.Fatal("no chat opened")
	}
	opened := m.msgs.Chat()
	m.Update(BatchMsg{Removed: []domain.ChatID{opened}, NamesChanged: true})
	view := ansi.Strip(m.View().Content)
	if m.msgs.Chat() != 0 || m.Focus() != FocusChatList || !strings.Contains(view, "merged") {
		t.Fatalf("removing the open chat must close it with a notice:\n%s", view)
	}
	if _, ok := m.chats.Title(opened); ok {
		t.Fatal("the removed chat must leave the list")
	}
	m.Update(BatchMsg{Notice: "Disk full"})
	if !strings.Contains(ansi.Strip(m.View().Content), "Disk full") {
		t.Fatal("notices must show in the status line")
	}
}

func TestRootShowsTheLinkScreen(t *testing.T) {
	var asked string
	m := New(Options{History: emptyHistory{}, Link: &linkscreen.Options{
		PairWithPhone: func(phone string) tea.Cmd { asked = phone; return nil },
	}})
	size(m, 120, 60)
	m.Update(BatchMsg{Link: &domain.LinkProgress{State: domain.LinkQR, QR: "2@" + strings.Repeat("q", 150)}})
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Link tarcin") || !strings.Contains(view, "█") {
		t.Fatalf("link screen:\n%s", view)
	}
	m.Update(key("p"))
	for _, r := range "905551112233" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if asked != "905551112233" {
		t.Fatalf("keys must reach the link screen: asked %q", asked)
	}
	m.Update(BatchMsg{Linked: true})
	view = ansi.Strip(m.View().Content)
	if strings.Contains(view, "Link tarcin") || !strings.Contains(view, "Linked") {
		t.Fatalf("after linking:\n%s", view)
	}
	m.Update(BatchMsg{Link: &domain.LinkProgress{State: domain.LinkQR, QR: "2@x"}, Linked: true})
	if strings.Contains(ansi.Strip(m.View().Content), "Link tarcin") {
		t.Fatal("a batch that also reports the link must not reopen the screen")
	}

	// Quit works on the link screen; without link actions there is none.
	m2 := New(Options{History: emptyHistory{}, Link: &linkscreen.Options{}})
	size(m2, 120, 60)
	m2.Update(BatchMsg{Link: &domain.LinkProgress{State: domain.LinkQR, QR: "2@x"}})
	if _, cmd := m2.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}); cmd == nil {
		t.Fatal("ctrl+q must quit from the link screen")
	}
	m3 := New(Options{History: emptyHistory{}})
	size(m3, 120, 60)
	m3.Update(BatchMsg{Link: &domain.LinkProgress{State: domain.LinkQR, QR: "2@x"}})
	if strings.Contains(ansi.Strip(m3.View().Content), "Link tarcin") {
		t.Fatal("without link actions the screen is not shown")
	}
}

func TestRootOpensOnTheNotice(t *testing.T) {
	begun := 0
	m := New(Options{History: emptyHistory{}, Link: &linkscreen.Options{
		Begin: func() tea.Cmd { begun++; return nil },
	}})
	size(m, 120, 60)
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "unofficial client") || !strings.Contains(view, "not linked") {
		t.Fatalf("the notice comes first:\n%s", view)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if begun != 1 || strings.Contains(ansi.Strip(m.View().Content), "not linked") {
		t.Fatalf("Enter must start linking: begun %d", begun)
	}
	m.Update(BatchMsg{Linked: true})
	m.Update(BatchMsg{Link: &domain.LinkProgress{State: domain.LinkQR, QR: "2@x"}})
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "unofficial client") || !strings.Contains(view, "Link tarcin") {
		t.Fatalf("linking again later skips the notice:\n%s", view)
	}

	// The hint and the help screen follow a rebound quit key.
	km, err := keymap.New(map[string]map[string][]string{"global": {"quit": {"ctrl+c"}}})
	if err != nil {
		t.Fatal(err)
	}
	m = New(Options{History: emptyHistory{}, Keymap: km, Link: &linkscreen.Options{Begin: func() tea.Cmd { return nil }}})
	size(m, 120, 60)
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "ctrl+c  quit") || strings.Contains(view, "ctrl+q") {
		t.Fatalf("the notice must show the quit key:\n%s", view)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(BatchMsg{Linked: true})
	m.Update(key("?"))
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl}); cmd != nil {
		t.Fatal("ctrl+q no longer quits from the help screen")
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd == nil {
		t.Fatal("ctrl+c must quit from the help screen")
	}
}

func TestBridgeCarriesLinking(t *testing.T) {
	s := newSink()
	b := NewBridge(s.send, BridgeOptions{FlushInterval: 5 * time.Millisecond})
	defer b.Close()
	b.Link(domain.LinkProgress{State: domain.LinkQR, QR: "a"})
	b.Link(domain.LinkProgress{State: domain.LinkQR, QR: "b"})
	b.Linked()
	got := s.wait(t)
	if got[0].Link == nil || got[0].Link.QR != "b" || !got[0].Linked {
		t.Fatalf("batch = %+v", got[0])
	}
}

func TestRootRetriesOnlyFailedOwnMessages(t *testing.T) {
	var retried []domain.WAMessageID
	m := New(Options{History: emptyHistory{}, Retry: func(msg domain.Message) tea.Cmd {
		retried = append(retried, msg.WAID)
		return nil
	}})
	size(m, 100, 30)
	failed := domain.Message{WAID: "F", FromMe: true, Status: domain.StatusFailed}
	m.Update(msglist.MessageActionMsg{Action: keymap.Retry, Message: failed})
	m.Update(msglist.MessageActionMsg{Action: keymap.Retry, Message: domain.Message{WAID: "S", FromMe: true, Status: domain.StatusSent}})
	if len(retried) != 1 || retried[0] != "F" {
		t.Fatalf("retried %v", retried)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "failed to send") {
		t.Fatal("retrying another message must say why nothing happens")
	}
}
