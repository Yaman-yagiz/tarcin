// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/composer"
)

// recordingUI records what the pipeline reports.
type recordingUI struct {
	mu       sync.Mutex
	appended map[domain.ChatID]int
	updated  []domain.Message
	rows     map[domain.ChatID]chatlist.Row
	reloads  map[domain.ChatID]int
	removed  []domain.ChatID
	groups   []domain.ChatID
	presence []domain.ChatID
	typingOn map[domain.ChatID]bool
	names    int
	notices  []string
	typing   int
	conn     []domain.ConnectionState
	links    []domain.LinkProgress
	linked   int
}

func newRecordingUI() *recordingUI {
	return &recordingUI{appended: map[domain.ChatID]int{}, rows: map[domain.ChatID]chatlist.Row{}, reloads: map[domain.ChatID]int{}}
}

func (r *recordingUI) Messages(chat domain.ChatID, msgs []domain.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appended[chat] += len(msgs)
}

func (r *recordingUI) Updates(_ domain.ChatID, msgs []domain.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updated = append(r.updated, msgs...)
}

func (r *recordingUI) Reload(chat domain.ChatID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloads[chat]++
}

func (r *recordingUI) RemoveChat(chat domain.ChatID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, chat)
	delete(r.rows, chat)
}

func (r *recordingUI) Presence(chat domain.ChatID, _ domain.PresenceChanged) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.presence = append(r.presence, chat)
}

func (r *recordingUI) GroupChanged(chat domain.ChatID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.groups = append(r.groups, chat)
}

func (r *recordingUI) Chat(row chatlist.Row) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[row.ID] = row
}

func (r *recordingUI) NamesChanged() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names++
}

func (r *recordingUI) Notice(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, text)
}

func (r *recordingUI) Typing(chat domain.ChatID, on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.typing++
	if r.typingOn == nil {
		r.typingOn = map[domain.ChatID]bool{}
	}
	r.typingOn[chat] = on
}

func (r *recordingUI) Connection(c domain.ConnectionChanged) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conn = append(r.conn, c.State)
}

func (r *recordingUI) Link(p domain.LinkProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.links = append(r.links, p)
}

func (r *recordingUI) Linked() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.linked++
}

func (r *recordingUI) row(id domain.ChatID) (chatlist.Row, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[id]
	return row, ok
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// newTestPipeline opens a pipeline in a temporary directory and starts
// it with a recording UI.
func newTestPipeline(t *testing.T, me fake.Person) (*pipeline, *recordingUI) {
	t.Helper()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{MeLID: me.LID, MePN: me.PN})
	if err != nil {
		t.Fatal(err)
	}
	r := newRecordingUI()
	if err := p.start(r); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.close(context.Background()) })
	return p, r
}

// runFake delivers a fake backend's whole run through a pipeline.
func runFake(t *testing.T, cfg fake.Config) (*pipeline, *recordingUI, *fake.Backend) {
	t.Helper()
	fb := fake.New(cfg)
	p, r := newTestPipeline(t, fb.Me())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := fb.Run(ctx, p); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("the fake backend did not finish")
	}
	return p, r, fb
}

func allMessages(t *testing.T, p *pipeline, chat domain.ChatID) []domain.Message {
	t.Helper()
	page, err := p.reader.Latest(t.Context(), chat, 37)
	if err != nil {
		t.Fatal(err)
	}
	all := page
	for len(page) > 0 {
		if page, err = p.reader.Before(t.Context(), chat, page[0].Cursor(), 37); err != nil {
			t.Fatal(err)
		}
		all = append(slices.Clone(page), all...)
	}
	return all
}

func TestPipelineIngestsAFakeSession(t *testing.T) {
	cfg := fake.Config{
		Seed: 3, Chats: 8, MembersPerGroup: 6, InitialHistory: 120, HotChatHistory: 900, HistoryChunk: 100,
		Rate: 3000, MaxEvents: 400, DuplicateRate: 0.3, ReorderWindow: 5, TypingRate: 0.2,
		PNAddressingRate: 0.3, Tick: time.Millisecond,
	}
	p, r, fb := runFake(t, cfg)

	chats, err := p.reader.Chats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != cfg.Chats {
		t.Fatalf("%d chats, want %d", len(chats), cfg.Chats)
	}
	total := 0
	for _, c := range chats {
		all := allMessages(t, p, c.ID)
		seen := map[domain.WAMessageID]bool{}
		for i, m := range all {
			if i > 0 && !all[i-1].Cursor().Before(m.Cursor()) {
				t.Fatalf("chat %d is not strictly ordered", c.ID)
			}
			if seen[m.WAID] {
				t.Fatalf("chat %d stores %s twice", c.ID, m.WAID)
			}
			seen[m.WAID] = true
		}
		total += len(all)
	}
	if want := 900 + 7*120 + 400; total != want {
		t.Fatalf("stored %d messages, want %d (history plus live, without duplicates)", total, want)
	}
	hot, ok, err := p.reader.ChatID(t.Context(), fb.Chats()[0].JID)
	if err != nil || !ok {
		t.Fatalf("hot group: %v, %v", ok, err)
	}
	waitUntil(t, "a row for every chat", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.rows) == cfg.Chats
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.typing == 0 || !slices.Contains(r.conn, domain.ConnOnline) || r.names == 0 || r.appended[hot] == 0 {
		t.Fatalf("notifications: typing %d, connection %v, names %d, hot appends %d", r.typing, r.conn, r.names, r.appended[hot])
	}
	for _, row := range r.rows {
		if row.Title == "" || strings.Contains(row.Title, "@") {
			t.Fatalf("row %d has no proper title: %q", row.ID, row.Title)
		}
	}
	ids, _ := p.reader.Identities(t.Context())
	for i := range ids {
		if ids[i].IsMe && p.names.DisplayName(ids[i].ID) != "You" {
			t.Fatal("my own name must show as You")
		}
	}
	if p.names.DisplayName(99999) != "" {
		t.Fatal("an unknown person has no name")
	}
}

func TestPipelineSendAndRead(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 3, AutoReceipts: true, Tick: time.Millisecond})
	p, r := newTestPipeline(t, fb.Me())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	waitUntil(t, "the initial sync", func() bool {
		chats, _ := p.reader.Chats(t.Context())
		return len(chats) == 3
	})
	chats, _ := p.reader.Chats(t.Context())
	chat := chats[0].ID

	s := newSender(fb, p)
	defer s.close()
	if cmd := p.send(s, fb)(ui.Outgoing{Chat: chat, Text: "merhaba"}); cmd != nil {
		t.Fatal("sending needs no command: the message joins its chat's queue")
	}
	waitUntil(t, "the sent message to be read", func() bool {
		page, _ := p.reader.Latest(t.Context(), chat, 1)
		return len(page) == 1 && page[0].Body == "merhaba" && page[0].FromMe && page[0].Status == domain.StatusRead
	})
	if len(fb.Sent()) != 1 {
		t.Fatal("the backend did not receive the message")
	}

	other := chats[1]
	incoming := func(waid string, at time.Time) {
		t.Helper()
		if err := p.Deliver(t.Context(), []domain.Event{domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
			ChatJID: other.JID, SenderJID: "777@lid", WAID: domain.WAMessageID(waid), Timestamp: at, Kind: domain.KindText, Body: "hi",
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	incoming("U1", time.Now())
	waitUntil(t, "an unread message", func() bool { row, _ := r.row(other.ID); return row.Unread >= 1 })
	if msg := p.read(other.ID)(); msg != nil {
		t.Fatalf("read returned %#v", msg)
	}
	waitUntil(t, "the read", func() bool { row, _ := r.row(other.ID); return row.Unread == 0 })
	// While the chat is open, its new messages are read as they arrive.
	incoming("U2", time.Now().Add(time.Second))
	time.Sleep(300 * time.Millisecond)
	waitUntil(t, "the open chat to stay read", func() bool {
		c, _ := p.reader.ChatsByID(t.Context(), []domain.ChatID{other.ID})
		return len(c) == 1 && c[0].UnreadCount == 0
	})

	p.send(s, fb)(ui.Outgoing{Chat: 99, Text: "x"})
	waitUntil(t, "a notice about the unknown chat", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.ContainsFunc(r.notices, func(n string) bool { return strings.HasPrefix(n, "Message not sent") })
	})
	if _, ok := p.read(99)().(composer.ErrorMsg); !ok {
		t.Fatal("reading an unknown chat must report an error")
	}
}

func TestPipelineUpdatesRowsAndMerges(t *testing.T) {
	p, r := newTestPipeline(t, fake.Person{LID: "1@lid", PN: "905550000000@s.whatsapp.net"})
	chat, peer := domain.JID("2@lid"), domain.JID("905551111111@s.whatsapp.net")
	ts := time.Now().Add(-time.Hour)
	if err := p.Deliver(t.Context(), []domain.Event{
		domain.IdentityLinked{PN: peer, LID: chat},
		domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
			ChatJID: peer, SenderJID: peer, WAID: "A", Timestamp: ts, Kind: domain.KindText, Body: "selam", PushName: "Ali",
		}},
		domain.MessageEdited{ChatJID: chat, TargetWAID: "A", Body: "selam!", EditedAt: ts.Add(time.Minute)},
		domain.MessageRevoked{ChatJID: chat, TargetWAID: "missing", RevokedAt: ts},
		domain.ChatUpdated{ChatJID: chat, Fields: domain.ChatFieldPinned | domain.ChatFieldMuted, PinnedAt: ts, MutedUntil: domain.MuteForever},
	}); err != nil {
		t.Fatal(err)
	}
	chats, _ := p.reader.Chats(t.Context())
	if len(chats) != 1 {
		t.Fatalf("a PN-addressed message and the LID must share one chat, got %d chats", len(chats))
	}
	id := chats[0].ID
	msgs, _ := p.reader.Latest(t.Context(), id, 10)
	if len(msgs) != 1 || msgs[0].Body != "selam!" || msgs[0].Revision == 0 {
		t.Fatalf("edit not applied: %+v", msgs)
	}
	waitUntil(t, "the row", func() bool {
		row, ok := r.row(id)
		return ok && row.Title == "Ali" && !row.PinnedAt.IsZero() && row.Muted && row.Unread == 1
	})

	// A person seen by number and by LID before the link: two chats
	// become one, and the UI drops the second.
	other, otherPN := domain.JID("3@lid"), domain.JID("905552222222@s.whatsapp.net")
	for i, jid := range []domain.JID{otherPN, other} {
		if err := p.Deliver(t.Context(), []domain.Event{domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
			ChatJID: jid, SenderJID: jid, WAID: domain.WAMessageID(fmt.Sprint("B", i)), Timestamp: ts, Kind: domain.KindText, Body: "x",
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	pnChat, _, _ := p.reader.ChatID(t.Context(), otherPN)
	if err := p.Deliver(t.Context(), []domain.Event{domain.IdentityLinked{PN: otherPN, LID: other}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the merged chat to leave the list", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.removed) == 1 && len(r.rows) == 2
	})
	if r.removed[0] == id || r.removed[0] == 0 {
		t.Fatalf("removed %v", r.removed)
	}
	merged, _, _ := p.reader.ChatID(t.Context(), other)
	if merged == pnChat {
		t.Fatal("the LID chat must survive")
	}
	if all := allMessages(t, p, merged); len(all) != 2 {
		t.Fatalf("merged chat has %d messages", len(all))
	}
}

func TestReadReceiptsFollowThePrivacySetting(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		fb := fake.New(fake.Config{Seed: 6, Chats: 1, Tick: time.Millisecond})
		ctx, cancel := context.WithCancel(t.Context())
		discard := backend.SinkFunc(func(context.Context, []domain.Event) error { return nil })
		go func() { _ = fb.Run(ctx, discard) }() // online, so receipts can be sent
		waitUntil(t, "the backend to connect", func() bool {
			return fb.MarkRead(ctx, "", "", nil, time.Now()) == nil
		})
		p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"),
			pipelineOptions{MeLID: "900@lid", Backend: fb, ReadReceipts: enabled})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.start(newRecordingUI()); err != nil {
			t.Fatal(err)
		}
		var evs []domain.Event
		for i, sender := range []domain.JID{"101@lid", "102@lid", "101@lid"} {
			evs = append(evs, domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
				ChatJID: "g@g.us", SenderJID: sender, WAID: domain.WAMessageID(fmt.Sprint("IN", i)),
				Timestamp: time.Now().Add(-time.Minute), Kind: domain.KindText, Body: "x"}})
		}
		if err := p.Deliver(t.Context(), evs); err != nil {
			t.Fatal(err)
		}
		chat, _, _ := p.reader.ChatID(t.Context(), "g@g.us")
		if msg := p.read(chat)(); msg != nil {
			t.Fatalf("read = %#v", msg)
		}
		if enabled {
			waitUntil(t, "the read receipts", func() bool { return len(fb.MarkedRead()) == 3 })
			time.Sleep(50 * time.Millisecond)
			if n := len(fb.MarkedRead()); n != 3 {
				t.Fatalf("%d receipts for 3 messages: a read must not report them twice", n)
			}
		} else {
			time.Sleep(50 * time.Millisecond)
			if n := len(fb.MarkedRead()); n != 0 {
				t.Fatalf("%d read receipts sent with the setting off", n)
			}
		}
		if refs, _ := p.reader.UnreadIncoming(t.Context(), chat, 10); len(refs) != 0 {
			t.Fatal("the chat is read either way")
		}
		_ = p.close(context.Background())
		cancel()
	}
}

// TestOpenChatStaysUnreadWhileAway checks that the open chat's new
// messages are not read, and send no read receipts, while the terminal
// lacks focus, and are read when the user is back.
func TestOpenChatStaysUnreadWhileAway(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 6, Chats: 1, Tick: time.Millisecond})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	discard := backend.SinkFunc(func(context.Context, []domain.Event) error { return nil })
	go func() { _ = fb.Run(ctx, discard) }() // online, so receipts can be sent
	waitUntil(t, "the backend to connect", func() bool {
		return fb.MarkRead(ctx, "", "", nil, time.Now()) == nil
	})
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"),
		pipelineOptions{MeLID: "900@lid", Backend: fb, ReadReceipts: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	incoming := func(waid string) {
		t.Helper()
		// A message in the same millisecond as a read counts as read: the
		// next one comes later, as it would from the server.
		time.Sleep(2 * time.Millisecond)
		if err := p.Deliver(t.Context(), []domain.Event{domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
			ChatJID: "101@lid", SenderJID: "101@lid", WAID: domain.WAMessageID(waid), Timestamp: time.Now(), Kind: domain.KindText, Body: "x",
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	incoming("A1")
	chat, _, _ := p.reader.ChatID(t.Context(), "101@lid")
	if msg := p.read(chat)(); msg != nil {
		t.Fatalf("read = %#v", msg)
	}
	waitUntil(t, "the first receipt", func() bool { return len(fb.MarkedRead()) == 1 })

	if cmd := p.attention(false); cmd != nil {
		t.Fatal("leaving needs no command")
	}
	incoming("A2")
	time.Sleep(100 * time.Millisecond)
	if refs, _ := p.reader.UnreadIncoming(t.Context(), chat, 10); len(refs) != 1 {
		t.Fatalf("%d unread messages while away, want 1", len(refs))
	}
	if n := len(fb.MarkedRead()); n != 1 {
		t.Fatalf("%d receipts: a message that arrived while away was reported read", n)
	}

	cmd := p.attention(true)
	if cmd == nil {
		t.Fatal("coming back to an open chat must read it")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("attention = %#v", msg)
	}
	waitUntil(t, "the receipt after coming back", func() bool { return len(fb.MarkedRead()) == 2 })
	if refs, _ := p.reader.UnreadIncoming(t.Context(), chat, 10); len(refs) != 0 {
		t.Fatal("the chat is read once the user is back")
	}
	// Back again, new messages are read as they arrive.
	incoming("A3")
	waitUntil(t, "the live receipt", func() bool { return len(fb.MarkedRead()) == 3 })
}

// TestRunDemoQuits runs the whole demo headless and quits it with ctrl+q.
func TestRunDemoQuits(t *testing.T) {
	t.Setenv("TARCIN_CACHE_DIR", t.TempDir())
	t.Setenv("TARCIN_CONFIG_DIR", t.TempDir())
	in, w := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunDemo(context.Background(), in, io.Discard, DemoOptions{
			Seed: 1, Rate: 50, ProgramOptions: []tea.ProgramOption{tea.WithWindowSize(120, 40)},
		})
	}()
	time.Sleep(500 * time.Millisecond)
	for _, k := range []string{"j", "j", "\r", "k", "G", "\x11"} { // navigate, open, quit
		if _, err := w.Write([]byte(k)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunDemo: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the demo did not quit")
	}
	_ = w.Close()
	if err := RunDemo(context.Background(), in, io.Discard, DemoOptions{Rate: -1}); err == nil {
		t.Fatal("a negative rate must be rejected")
	}
}

// TestFramesAgreeOnWidth renders real demo frames in wcwidth mode and
// checks that every line measures the same under wcwidth and grapheme
// widths, so a terminal using either model draws the frame unshifted.
func TestFramesAgreeOnWidth(t *testing.T) {
	cfg := fake.Config{Seed: 9, Chats: 12, MembersPerGroup: 8, InitialHistory: 300, HistoryChunk: 300, Rate: 4000, MaxEvents: 300, Tick: time.Millisecond}
	p, r, _ := runFake(t, cfg)
	waitUntil(t, "the rows", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.rows) == cfg.Chats
	})
	r.mu.Lock()
	var rows []chatlist.Row
	for _, row := range r.rows {
		rows = append(rows, row)
	}
	r.mu.Unlock()
	m := ui.New(ui.Options{History: p.reader, Names: p.names})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(chatlist.LoadMsg{Rows: rows})

	check := func(stage string) {
		t.Helper()
		for i, line := range strings.Split(m.View().Content, "\n") {
			wc, gr := ansi.WcWidth.StringWidth(line), ansi.GraphemeWidth.StringWidth(line)
			if wc != 120 || gr != 120 {
				t.Fatalf("%s: line %d is %d cells by wcwidth and %d by graphemes: %q", stage, i, wc, gr, ansi.Strip(line))
			}
		}
	}
	press := func(code rune, text string) {
		k := tea.KeyPressMsg{Code: code, Text: text}
		if code == 'b' && text == "" {
			k = tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}
		}
		_, cmd := m.Update(k)
		for cmd != nil {
			msg := cmd()
			if b, ok := msg.(tea.BatchMsg); ok {
				for _, c := range b {
					if c != nil {
						m.Update(c())
					}
				}
				break
			}
			_, cmd = m.Update(msg)
		}
	}
	check("chat list")
	for chat := range 12 {
		press(tea.KeyEnter, "")
		check(fmt.Sprintf("chat %d", chat))
		for range 5 {
			press('b', "") // ctrl+b: page up
		}
		check(fmt.Sprintf("chat %d scrolled", chat))
		press(tea.KeyTab, "")
		press(tea.KeyTab, "") // back to the chat list
		press('j', "j")
	}
}

func TestPipelineLinksADevice(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 5, Chats: 2, Link: true, LinkAfter: 20 * time.Millisecond, Tick: time.Millisecond})
	p, r := newTestPipeline(t, fake.Person{}) // the account is not known before linking
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	waitUntil(t, "the link", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.linked == 1 && len(r.links) > 0 && r.links[0].State == domain.LinkQR
	})
	waitUntil(t, "the account to show as You", func() bool {
		ids, _ := p.reader.Identities(t.Context())
		for i := range ids {
			if ids[i].IsMe && ids[i].LID == fb.Me().LID && p.names.DisplayName(ids[i].ID) == "You" {
				return true
			}
		}
		return false
	})
}

// failingLinker fails linking until it was retried.
type failingLinker struct {
	fake.Backend
	runs atomic.Int32
}

func (f *failingLinker) Run(ctx context.Context, _ backend.Sink) error {
	if f.runs.Add(1) == 1 {
		return fmt.Errorf("codes expired: %w", backend.ErrLinkFailed)
	}
	<-ctx.Done()
	return nil
}

func TestRunBackendRetriesOnRequest(t *testing.T) {
	b := &failingLinker{}
	retry := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runBackend(ctx, b, nil, retry) }()
	time.Sleep(20 * time.Millisecond)
	if n := b.runs.Load(); n != 1 {
		t.Fatalf("runs = %d: after a failed link, wait for the user", n)
	}
	opts := linkOptions(fake.New(fake.Config{}), retry)
	opts.Retry()
	waitUntil(t, "the second attempt", func() bool { return b.runs.Load() == 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestGroupRefresh: opening a group fetches it when its stored member
// list is missing or older than a day, through the job pool; a change to
// a group's members does too. A fresh list is not fetched again.
func TestGroupRefresh(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()) }
	later := func(d time.Duration) { now.Add(int64(d)) }
	fb := fake.New(fake.Config{Seed: 6, Chats: 4, GroupRatio: 0.5, MembersPerGroup: 3, Tick: time.Millisecond})
	me := fb.Me()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{
		MeLID: me.LID, MePN: me.PN, Backend: fb, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newRecordingUI()
	if err := p.start(r); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	waitUntil(t, "the initial sync", func() bool {
		chats, _ := p.reader.Chats(t.Context())
		return len(chats) == 4
	})
	var g domain.Chat
	chats, _ := p.reader.Chats(t.Context())
	for _, c := range chats {
		if c.Kind == domain.ChatGroup {
			g = c
		}
	}
	// The initial sync stored the full list just now: opening fetches nothing.
	_ = p.read(g.ID)()
	time.Sleep(50 * time.Millisecond)
	if n := len(fb.FetchedGroups()); n != 0 {
		t.Fatalf("a fresh group was fetched %d times", n)
	}
	// A day later, opening fetches it once, however often it is opened.
	later(25 * time.Hour)
	_ = p.read(g.ID)()
	_ = p.read(g.ID)()
	waitUntil(t, "the fetch", func() bool {
		meta, _, _ := p.reader.GroupMeta(t.Context(), g.ID)
		return strings.HasPrefix(meta.Topic, "A simulated group")
	})
	if got := fb.FetchedGroups(); len(got) != 1 || got[0] != g.JID {
		t.Fatalf("fetched = %v", got)
	}
	waitUntil(t, "the members view to hear of it", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.Contains(r.groups, g.ID)
	})
	page, _ := p.reader.GroupMembers(t.Context(), g.ID, 0, 10)
	if len(page) != 4 || page[0].Identity == 0 {
		t.Fatalf("members = %+v", page)
	}
	// A member change to a stale group fetches it in full.
	later(25 * time.Hour)
	if err := p.Deliver(t.Context(), []domain.Event{domain.GroupMembersChanged{ChatJID: g.JID, Left: []domain.JID{"999@lid"}}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the second fetch", func() bool { return len(fb.FetchedGroups()) == 2 })
	// A group the account left is not asked for again.
	p.refreshGroup("120363000000000000@g.us")
	waitUntil(t, "the failed fetch", func() bool { return p.jobs.Stats().Failed == 1 })
	if n := len(fb.FetchedGroups()); n != 3 {
		t.Fatalf("a gone group must not be retried: %d fetches", n)
	}
}

// TestOlderHistoryOnDemand: asking for a chat's older messages stores
// what the phone answers, until the phone has none and the chat's
// history is complete.
func TestOlderHistoryOnDemand(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 8, Chats: 2, GroupRatio: 0.01, InitialHistory: 5, OnDemandHistory: 60, Tick: time.Millisecond})
	p, _ := newTestPipeline(t, fb.Me())
	p.opts.Backend = fb
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	var chat domain.ChatID
	waitUntil(t, "the history", func() bool {
		chats, _ := p.reader.Chats(t.Context())
		for _, c := range chats {
			if c.Kind == domain.ChatDirect && len(allMessages(t, p, c.ID)) == 5 {
				chat = c.ID
				return true
			}
		}
		return false
	})
	older := p.older()
	ask := func() string {
		t.Helper()
		oldest := allMessages(t, p, chat)[0]
		msg := older(chat, oldest)()
		b, _ := msg.(ui.BatchMsg)
		return b.Notice
	}
	if n := ask(); !strings.Contains(n, "Asking your phone") {
		t.Fatalf("notice = %q", n)
	}
	waitUntil(t, "50 older messages", func() bool { return len(allMessages(t, p, chat)) == 55 })
	ask()
	waitUntil(t, "the rest and the end of the history", func() bool {
		c, _ := p.reader.ChatsByID(t.Context(), []domain.ChatID{chat})
		return len(allMessages(t, p, chat)) == 65 && c[0].HistoryComplete
	})
	if n := ask(); n != "This is the start of the chat" {
		t.Fatalf("notice = %q", n)
	}
	if refs, _ := p.reader.UnreadIncoming(t.Context(), chat, 100); len(refs) != 0 {
		t.Fatalf("older messages are never unread: %d", len(refs))
	}
}

// TestReact: a reaction is sent with the target's sender and stored as
// this account's, so it shows; an empty one removes it.
func TestReact(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 2, GroupRatio: 1, MembersPerGroup: 3, InitialHistory: 3, Tick: time.Millisecond})
	p, _ := newTestPipeline(t, fb.Me())
	p.opts.Backend = fb
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	var chat domain.Chat
	waitUntil(t, "the history", func() bool {
		chats, _ := p.reader.Chats(t.Context())
		for _, c := range chats {
			if len(allMessages(t, p, c.ID)) == 3 {
				chat = c
				return true
			}
		}
		return false
	})
	target := allMessages(t, p, chat.ID)[0]
	react := p.react()
	if msg := react(target, "\U0001F44D")(); msg != nil {
		t.Fatalf("react = %#v", msg)
	}
	if got := fb.Reacted(); len(got) != 1 || got[0].TargetWAID != target.WAID || got[0].Emoji != "\U0001F44D" {
		t.Fatalf("sent = %+v", got)
	}
	waitUntil(t, "the stored reaction", func() bool {
		m, _, _ := p.reader.MessageByWAID(t.Context(), chat.ID, target.WAID)
		return len(m.Reactions) == 1 && m.Reactions[0].Mine && m.Reactions[0].Count == 1
	})
	_ = react(target, "")()
	waitUntil(t, "the removal", func() bool {
		m, _, _ := p.reader.MessageByWAID(t.Context(), chat.ID, target.WAID)
		return len(m.Reactions) == 0
	})
}

// TestEditAndRevoke: this account's messages are edited within the edit
// window and deleted for everyone; someone else's only as a group admin.
func TestEditAndRevoke(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	fb := fake.New(fake.Config{Seed: 4, Chats: 2, GroupRatio: 1, MembersPerGroup: 3, Tick: time.Millisecond})
	me := fb.Me()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{
		MeLID: me.LID, MePN: me.PN, Backend: fb, Now: func() time.Time { return time.Unix(0, now.Load()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	group := fb.Chats()[0]
	at := time.Unix(0, now.Load()).Truncate(time.Second)
	if err := p.Deliver(t.Context(), []domain.Event{
		domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{ChatJID: group.JID, SenderJID: me.LID,
			WAID: "MINE", FromMe: true, Timestamp: at, Kind: domain.KindText, Body: "selm"}},
		domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{ChatJID: group.JID, SenderJID: group.Members[2].LID,
			WAID: "THEIRS", Timestamp: at, Kind: domain.KindText, Body: "spam"}},
	}); err != nil {
		t.Fatal(err)
	}
	chat, _, _ := p.reader.ChatID(t.Context(), group.JID)
	get := func(waid domain.WAMessageID) domain.Message {
		m, _, _ := p.reader.MessageByWAID(t.Context(), chat, waid)
		return m
	}
	notice := func(msg tea.Msg) string {
		b, _ := msg.(ui.BatchMsg)
		return b.Notice
	}
	waitUntil(t, "the online state", func() bool { return fb.EditText(t.Context(), group.JID, "", "") == nil })

	if n := notice(p.edit()(get("MINE"), "selam")()); n != "" {
		t.Fatalf("edit: %q", n)
	}
	waitUntil(t, "the stored edit", func() bool { m := get("MINE"); return m.Body == "selam" && !m.EditedAt.IsZero() })
	if n := notice(p.edit()(get("THEIRS"), "x")()); !strings.Contains(n, "Only your own") {
		t.Fatalf("editing someone else's: %q", n)
	}
	now.Add(int64(time.Hour))
	if n := notice(p.edit()(get("MINE"), "late")()); !strings.Contains(n, "20 minutes") {
		t.Fatalf("a late edit: %q", n)
	}

	if n := notice(p.revoke()(get("THEIRS"))()); !strings.Contains(n, "group admin") {
		t.Fatalf("a member deleting someone else's: %q", n)
	}
	if n := notice(p.revoke()(get("MINE"))()); n != "" {
		t.Fatalf("revoke: %q", n)
	}
	waitUntil(t, "the stored deletion", func() bool { m := get("MINE"); return m.IsRevoked() })
	// As an admin.
	if err := p.Deliver(t.Context(), []domain.Event{domain.GroupMembersChanged{ChatJID: group.JID,
		Members: []domain.GroupMember{{JID: me.LID, AltJID: me.PN, Role: domain.RoleAdmin}}}}); err != nil {
		t.Fatal(err)
	}
	if n := notice(p.revoke()(get("THEIRS"))()); n != "" {
		t.Fatalf("an admin revoke: %q", n)
	}
	waitUntil(t, "the admin deletion", func() bool { m := get("THEIRS"); return m.IsRevoked() })
	revokes := fb.Revoked()
	if len(revokes) != 2 || revokes[0].Actor != "" || revokes[1].Actor != group.Members[2].LID || !revokes[1].ByAdmin {
		t.Fatalf("revokes = %+v", revokes)
	}
	now.Add(int64(72 * time.Hour))
	_ = p.Deliver(t.Context(), []domain.Event{domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
		ChatJID: group.JID, SenderJID: me.LID, WAID: "OLD", FromMe: true, Timestamp: at, Kind: domain.KindText, Body: "eski"}}})
	if n := notice(p.revoke()(get("OLD"))()); !strings.Contains(n, "two days") {
		t.Fatalf("a late deletion: %q", n)
	}
}

// TestPresenceAndTyping: an opened direct chat follows the peer's online
// status; a typing indicator ends by itself; typing and the online
// status are sent only as the privacy settings allow.
func TestPresenceAndTyping(t *testing.T) {
	old := typingTimeout
	typingTimeout = 30 * time.Millisecond
	defer func() { typingTimeout = old }()
	fb := fake.New(fake.Config{Seed: 4, Chats: 2, GroupRatio: 0.01, Tick: time.Millisecond})
	p, r := newTestPipeline(t, fb.Me())
	p.opts.Backend, p.opts.Typing, p.opts.Presence = fb, true, true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	peer := fb.Chats()[1]
	var chat domain.ChatID
	waitUntil(t, "the chat", func() bool {
		id, ok, _ := p.reader.ChatID(t.Context(), peer.JID)
		chat = id
		return ok
	})
	_ = p.read(chat)()
	waitUntil(t, "the peer's online status", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.Contains(r.presence, chat)
	})
	if err := p.Deliver(t.Context(), []domain.Event{domain.ChatTyping{ChatJID: peer.JID, SenderJID: peer.Peer.LID, Typing: true}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the typing indicator to end by itself", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.typing >= 2 && !r.typingOn[chat]
	})
	_ = p.typing()(chat, true)()
	_ = p.presence()(true)()
	p.offline()
	if avail, typing := fb.Presence(); !slices.Equal(avail, []bool{true, false}) || !slices.Equal(typing, []bool{true}) {
		t.Fatalf("sent online %v, typing %v", avail, typing)
	}
	p.opts.Typing, p.opts.Presence = false, false
	if p.typing() != nil || p.presence() != nil {
		t.Fatal("with the settings off, nothing is sent")
	}
}

// TestChatSettings: muting, pinning and archiving go to the account's
// app state and are stored at once; at most three chats are pinned.
func TestChatSettings(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 5, Tick: time.Millisecond})
	p, _ := newTestPipeline(t, fb.Me())
	p.opts.Backend = fb
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	waitUntil(t, "the chats", func() bool { c, _ := p.reader.Chats(t.Context()); return len(c) == 5 })
	waitUntil(t, "the connection", func() bool { return fb.PinChat(t.Context(), "x@g.us", false) == nil })
	chats, _ := p.reader.Chats(t.Context())
	set := p.chatSetting()
	do := func(chat domain.ChatID, s ui.ChatSetting) string {
		b, _ := set(chat, s)().(ui.BatchMsg)
		return b.Notice
	}
	get := func(id domain.ChatID) domain.Chat {
		c, _ := p.reader.ChatsByID(t.Context(), []domain.ChatID{id})
		return c[0]
	}
	if n := do(chats[0].ID, ui.ChatSetting{What: "mute", MuteFor: 8 * time.Hour}); !strings.HasPrefix(n, "Muted until") {
		t.Fatalf("mute: %q", n)
	}
	if c := get(chats[0].ID); !c.IsMuted(time.Now()) || c.MutedUntil.Equal(domain.MuteForever) {
		t.Fatalf("muted until %v", c.MutedUntil)
	}
	do(chats[0].ID, ui.ChatSetting{What: "mute"})
	if c := get(chats[0].ID); !c.MutedUntil.Equal(domain.MuteForever) {
		t.Fatalf("muted forever: %v", c.MutedUntil)
	}
	do(chats[0].ID, ui.ChatSetting{What: "unmute"})
	if c := get(chats[0].ID); c.IsMuted(time.Now()) {
		t.Fatal("unmuted")
	}
	for i := range 3 {
		if n := do(chats[i].ID, ui.ChatSetting{What: "pin"}); n != "Pinned" {
			t.Fatalf("pin %d: %q", i, n)
		}
	}
	if n := do(chats[3].ID, ui.ChatSetting{What: "pin"}); !strings.Contains(n, "At most three") {
		t.Fatalf("a fourth pin: %q", n)
	}
	do(chats[1].ID, ui.ChatSetting{What: "archive"})
	if c := get(chats[1].ID); !c.Archived || c.IsPinned() {
		t.Fatalf("archived and unpinned: %+v", c)
	}
	do(chats[1].ID, ui.ChatSetting{What: "unarchive"})
	if get(chats[1].ID).Archived {
		t.Fatal("unarchived")
	}
	var whats []string
	for _, s := range fb.Settings()[1:] { // the first is the connection probe
		whats = append(whats, s.What)
	}
	if want := []string{"mute", "mute", "mute", "pin", "pin", "pin", "archive", "archive"}; !slices.Equal(whats, want) {
		t.Fatalf("app state = %v, want %v", whats, want)
	}
}

func TestMetrics(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 2, InitialHistory: 5, Tick: time.Millisecond})
	p, _ := runFakeWith(t, fb)
	msg, ok := p.metrics(func() (int, int) { return 7, 1 })()().(ui.MetricsMsg)
	if !ok {
		t.Fatal("no metrics")
	}
	var titles []string
	for _, s := range msg.Sections {
		titles = append(titles, s.Title)
	}
	if want := []string{"Store", "Applier", "Network jobs", "Bridge"}; !slices.Equal(titles, want) {
		t.Fatalf("sections = %v, want %v", titles, want)
	}
	if v := msg.Sections[0].Items[1][1]; v == "0" {
		t.Fatal("the store's transactions must be counted")
	}
}

// runFakeWith delivers a fake backend's whole run through a pipeline.
func runFakeWith(t *testing.T, fb *fake.Backend) (*pipeline, *recordingUI) {
	t.Helper()
	p, r := newTestPipeline(t, fb.Me())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	waitUntil(t, "the history", func() bool { c, _ := p.reader.Chats(t.Context()); return len(c) == 2 })
	return p, r
}
