// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/sys/unix"

	"github.com/Yaman-yagiz/tarcin/internal/devtools/gen"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/store"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
)

// The storage benchmarks measure budgets B1, B2, B6 and B7 of
// docs/architecture/performance.md on the SQLite store. They are skipped
// unless TARCIN_STORAGEBENCH=1 (make storagebench).
//
// One dataset covers them all: 1,000 chats (B1), 100,000 messages of
// which 10,000 are in one chat (B2), and that chat a group of 5,000
// members (B7).
var benchSpec = gen.Spec{
	Name: "storage", Seed: 21, Chats: 1000, Messages: 100_000, HotChatMessages: 10_000,
	HotGroupMembers: 5_000, GroupRatio: 0.4, MembersPerGroup: 30,
}

// datasetEvents turns a dataset into the events of a history sync:
// people, chats and group members first, then the messages of each chat
// in chunks of 500, oldest first.
func datasetEvents(d *gen.Dataset, deliver func([]domain.Event, bool) error) error {
	jid := func(id domain.IdentityID) domain.JID { return d.Identities[id-1].CanonicalJID() }
	var batch []domain.Event
	flush := func(bulk bool) error {
		if len(batch) == 0 {
			return nil
		}
		err := deliver(batch, bulk)
		batch = nil
		return err
	}
	for i := range d.Identities {
		id := &d.Identities[i]
		batch = append(batch, domain.IdentityLinked{PN: id.PN, LID: id.LID},
			domain.ContactUpdated{JID: id.LID, PushName: id.PushName})
		if len(batch) >= 500 {
			if err := flush(false); err != nil {
				return err
			}
		}
	}
	for i := range d.Chats {
		c := &d.Chats[i]
		batch = append(batch, domain.ChatUpdated{ChatJID: c.JID, Fields: domain.ChatFieldTitle | domain.ChatFieldPinned |
			domain.ChatFieldArchived | domain.ChatFieldMuted | domain.ChatFieldUnread,
			Title: c.Title, PinnedAt: c.PinnedAt, Archived: c.Archived, MutedUntil: c.MutedUntil, UnreadCount: c.UnreadCount})
		if members := d.Members[c.ID]; len(members) > 0 {
			gm := domain.GroupMembersChanged{ChatJID: c.JID, Full: true}
			for _, m := range members {
				gm.Members = append(gm.Members, domain.GroupMember{JID: jid(m)})
			}
			batch = append(batch, gm)
		}
		if err := flush(false); err != nil {
			return err
		}
	}
	for i := range d.Chats {
		c := &d.Chats[i]
		for m := range d.Messages(i) {
			sender := jid(m.SenderID)
			batch = append(batch, domain.MessageReceived{Source: domain.SourceHistory, Message: domain.IncomingMessage{
				ChatJID: c.JID, SenderJID: sender, WAID: m.WAID, FromMe: m.FromMe, Timestamp: m.Timestamp,
				Kind: m.Kind, Body: m.Body, QuotedWAID: m.QuotedWAID, Flags: m.Flags, Media: m.Media,
			}})
			if len(batch) == 500 {
				if err := flush(true); err != nil {
					return err
				}
			}
		}
		if err := flush(true); err != nil {
			return err
		}
	}
	return nil
}

type importResult struct {
	messages int
	elapsed  time.Duration
	stats    store.WriterStats
}

// importDataset writes a dataset through the writer, as a history sync
// does: one chunk at a time, each waiting for its commit.
func importDataset(t *testing.T, path string, d *gen.Dataset) importResult {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, path, store.RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := store.Migrate(ctx, db, store.Migrations(), store.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	w, err := store.NewWriter(db, store.WriterOptions{Applier: store.NewEventApplier(store.ApplierOptions{MeLID: d.Me.LID, MePN: d.Me.PN})})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = datasetEvents(d, func(evs []domain.Event, bulk bool) error {
		if bulk {
			return w.SubmitBulk(ctx, evs)
		}
		return w.SubmitLive(ctx, evs)
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	stats := w.Stats()
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	return importResult{messages: d.TotalMessages(), elapsed: elapsed, stats: stats}
}

// liveThroughput measures single-message submissions from several
// goroutines, as live messages arrive from whatsmeow's handlers.
func liveThroughput(t *testing.T, n, submitters int) time.Duration {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "live.db"), store.RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := store.Migrate(ctx, db, store.Migrations(), store.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	w, err := store.NewWriter(db, store.WriterOptions{Applier: store.NewEventApplier(store.ApplierOptions{})})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(ctx) }()
	start := time.Now()
	var wg sync.WaitGroup
	for s := range submitters {
		wg.Go(func() {
			for i := s; i < n; i += submitters {
				ev := domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
					ChatJID: domain.JID(fmt.Sprintf("g%d@g.us", i%20)), SenderJID: domain.JID(fmt.Sprintf("%d@lid", i%300)),
					WAID: domain.WAMessageID(fmt.Sprint("L", i)), Timestamp: time.Now(), Kind: domain.KindText, Body: "a live message",
				}}
				if err := w.SubmitLive(ctx, []domain.Event{ev}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	return time.Since(start)
}

// rowsUI counts the chat rows the pipeline publishes.
type rowsUI struct {
	recordingUI
	want int
	full chan struct{}
	once sync.Once
}

func (r *rowsUI) Chat(row chatlist.Row) {
	r.recordingUI.Chat(row)
	r.mu.Lock()
	n := len(r.rows)
	r.mu.Unlock()
	if n == r.want {
		r.once.Do(func() { close(r.full) })
	}
}

// dropCache asks the kernel to drop the cached pages of the database
// files, so the next start reads them from the disk. It needs no root,
// unlike dropping the whole page cache.
func dropCache(t *testing.T, path string) {
	t.Helper()
	for _, f := range []string{path, path + "-wal"} {
		fd, err := unix.Open(f, unix.O_RDONLY, 0)
		if err != nil {
			continue
		}
		_ = unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED)
		_ = unix.Close(fd)
	}
}

// coldStart opens the store, loads every chat into the UI and renders the
// first frame: budget B1. With cold set, the database's pages are dropped
// from the page cache first.
func coldStart(t *testing.T, path string, chats int, cold bool) (time.Duration, []chatlist.Row) {
	t.Helper()
	if cold {
		dropCache(t, path)
	}
	start := time.Now()
	p, err := openPipeline(context.Background(), path, pipelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := &rowsUI{recordingUI: *newRecordingUI(), want: chats, full: make(chan struct{})}
	if err := p.start(r); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.full:
	case <-time.After(30 * time.Second):
		t.Fatal("the chat list did not load")
	}
	r.mu.Lock()
	rows := slices.Collect(maps.Values(r.rows))
	r.mu.Unlock()
	m := ui.New(ui.Options{History: p.reader, Names: p.names})
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m.Update(chatlist.LoadMsg{Rows: rows})
	_ = m.View()
	elapsed := time.Since(start)
	if err := p.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return elapsed, rows
}

// drive feeds a command's messages back into the model until it settles.
func drive(m *ui.Model, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				drive(m, c)
			}
			return
		}
		_, cmd = m.Update(msg)
	}
}

// openChat measures opening a chat until its first page is rendered:
// budget B2.
func openChat(m *ui.Model, chat domain.ChatID) time.Duration {
	start := time.Now()
	_, cmd := m.Update(chatlist.OpenMsg{ID: chat})
	drive(m, cmd)
	_ = m.View()
	return time.Since(start)
}

// TestStorageRSSHelper opens a database as tarcin does, opens the busy
// group, scrolls back through it and prints its memory use, for B7. It
// runs in its own process, so the test framework and the dataset do not
// count.
func TestStorageRSSHelper(t *testing.T) {
	path := os.Getenv("TARCIN_STORAGEBENCH_RSS")
	if path == "" {
		t.Skip("helper process only")
	}
	_, rows := coldStart(t, path, 1000, false)
	p, err := openPipeline(context.Background(), path, pipelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	m := ui.New(ui.Options{History: p.reader, Names: p.names})
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m.Update(chatlist.LoadMsg{Rows: rows})
	openChat(m, 1)
	for range 60 { // about 6,000 messages back: the window slides
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
		drive(m, cmd)
		_ = m.View()
	}
	runtime.GC()
	fmt.Printf("rss %.1f\n", rssMB())
	fmt.Printf("hwm %.1f\n", hwmMB())
	_ = p.close(context.Background())
}

func hwmMB() float64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for line := range strings.Lines(string(data)) {
		if v, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			kb, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 64)
			return kb / 1024
		}
	}
	return 0
}

func measureRSS(t *testing.T, path string) (rss, hwm float64) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStorageRSSHelper$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), "TARCIN_STORAGEBENCH_RSS="+path)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("RSS helper: %v\n%s", err, out)
	}
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) == 2 {
			v, _ := strconv.ParseFloat(f[1], 64)
			switch f[0] {
			case "rss":
				rss = v
			case "hwm":
				hwm = v
			}
		}
	}
	return rss, hwm
}

func TestStorageBenchmarks(t *testing.T) {
	if os.Getenv("TARCIN_STORAGEBENCH") != "1" {
		t.Skip("set TARCIN_STORAGEBENCH=1 to run the storage benchmarks (make storagebench)")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "tarcin.db")
	d := gen.Build(benchSpec)
	imp := importDataset(t, path, d)
	info, _ := os.Stat(path)

	const liveN, submitters = 20_000, 8
	live := liveThroughput(t, liveN, submitters)

	var starts, coldStarts []time.Duration
	var rows []chatlist.Row
	for range 20 {
		el, r := coldStart(t, path, benchSpec.Chats, false)
		starts, rows = append(starts, el), r
		el, _ = coldStart(t, path, benchSpec.Chats, true)
		coldStarts = append(coldStarts, el)
	}

	p, err := openPipeline(context.Background(), path, pipelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	m := ui.New(ui.Options{History: p.reader, Names: p.names})
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m.Update(chatlist.LoadMsg{Rows: rows})
	var opens []time.Duration
	for i := range 60 {
		other := domain.ChatID(2 + i%50)
		openChat(m, other)
		opens = append(opens, openChat(m, 1)) // the 10,000-message group
	}
	pageLen := msglist.PageSize
	_ = p.close(context.Background())

	rss, hwm := measureRSS(t, path)

	// B7 must stay flat as the database grows: the same account with
	// three times the messages.
	bigSpec := benchSpec
	bigSpec.Messages = 300_000
	bigPath := filepath.Join(dir, "big.db")
	bigImp := importDataset(t, bigPath, gen.Build(bigSpec))
	bigRSS, bigHWM := measureRSS(t, bigPath)

	b1, b1cold, b2 := summarize(starts), summarize(coldStarts), summarize(opens)
	var r strings.Builder
	fmt.Fprintf(&r, "# Storage benchmarks — %s\n\n%s\n", time.Now().Format("2006-01-02"), machine())
	fmt.Fprintf(&r, "- Data: generated account (seed %d): %d chats, %d messages, %d in a group of %d members;\n",
		benchSpec.Seed, benchSpec.Chats, benchSpec.Messages, benchSpec.HotChatMessages, benchSpec.HotGroupMembers)
	fmt.Fprintf(&r, "  database %.0f MB on disk after the import\n- SQLite %s\n\n", float64(info.Size())/1e6, store.Version())
	r.WriteString("| Measurement | Samples | p50 | p95 | p99 | max | Budget |\n|---|---|---|---|---|---|---|\n")
	fmt.Fprintf(&r, "%s\n", b1cold.row("B1: start until the chat list is drawn (ms, database not in the page cache)", "≤ 300 ms p95"))
	fmt.Fprintf(&r, "%s\n", b1.row("B1: the same with a warm page cache (ms)", "—"))
	fmt.Fprintf(&r, "%s\n", b2.row(fmt.Sprintf("B2: open the 10,000-message chat until its first %d messages are drawn (ms)", pageLen), "≤ 50 ms p95"))
	r.WriteString("\n| Measurement | Value | Budget |\n|---|---|---|\n")
	fmt.Fprintf(&r, "| B6: history import, chunks of 500 | %.0f msg/s (%d messages in %.1f s, %d transactions) | ≥ 5,000 msg/s |\n",
		float64(imp.messages)/imp.elapsed.Seconds(), imp.messages, imp.elapsed.Seconds(), imp.stats.Transactions)
	fmt.Fprintf(&r, "| B6: history import, 300,000 messages | %.0f msg/s | ≥ 5,000 msg/s |\n", float64(bigImp.messages)/bigImp.elapsed.Seconds())
	fmt.Fprintf(&r, "| Live messages, one per submission, %d submitters | %.0f msg/s | — |\n", submitters, float64(liveN)/live.Seconds())
	fmt.Fprintf(&r, "| B7: RSS with the 5,000-member group open and scrolled back, 100k messages | %.0f MB (peak %.0f MB) | ≤ 150 MB |\n", rss, hwm)
	fmt.Fprintf(&r, "| B7: the same with 300k messages | %.0f MB (peak %.0f MB) | flat |\n", bigRSS, bigHWM)
	report := r.String()
	t.Log("\n" + report)
	if out := os.Getenv("TARCIN_STORAGEBENCH_REPORT"); out != "" {
		if err := os.WriteFile(out, []byte(report), 0o600); err != nil { //nolint:gosec // the path the developer asked for
			t.Fatal(err)
		}
	}
}
