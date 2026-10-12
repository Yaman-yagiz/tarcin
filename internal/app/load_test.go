// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
)

// The load harness drives the real Bubble Tea program end to end under
// the scenarios of docs/architecture/performance.md §4 while keys are
// typed: S5, 100 msg/s into a busy group of 5,000 members plus 20 msg/s
// spread over other chats; S4, paging through that group's members; S6,
// navigating during a 50k-message history import; S7, switching chats
// while media downloads; and S8, a network flap every 10 s. It is skipped
// unless TARCIN_LOADTEST=1 (make loadtest).
//
// Latencies are measured from writing a key into the program's input to
// (a) the end of the View that follows the key's Update ("ready") and
// (b) the next write to the terminal output ("flushed"), which includes
// waiting for Bubble Tea's frame ticker.

// probe wraps the root model and timestamps key handling.
type probe struct {
	inner *ui.Model

	mu       sync.Mutex
	injected []time.Time // FIFO of key injection times
	awaiting []time.Time // keys handled whose frame has not been viewed yet
	ready    []time.Duration
	readyAt  []time.Time // when each ready sample's frame was viewed
	views    []time.Duration
	batches  []time.Duration
	last     string // previous frame, to skip keys that changed nothing
	skipped  int
	// disconnects counts the disconnections the UI was told about.
	disconnects int
}

func (p *probe) Init() tea.Cmd { return p.inner.Init() }

func (p *probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	start := time.Now()
	_, cmd := p.inner.Update(msg)
	p.mu.Lock()
	defer p.mu.Unlock()
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if len(p.injected) > 0 {
			p.awaiting = append(p.awaiting, p.injected[0])
			p.injected = p.injected[1:]
		}
	case ui.BatchMsg:
		p.batches = append(p.batches, time.Since(start))
		if c := msg.Connection; c != nil && c.State == domain.ConnDisconnected {
			p.disconnects++
		}
	}
	return p, cmd
}

func (p *probe) View() tea.View {
	start := time.Now()
	v := p.inner.View()
	now := time.Now()
	p.mu.Lock()
	p.views = append(p.views, now.Sub(start))
	changed := v.Content != p.last
	p.last = v.Content
	for _, at := range p.awaiting {
		if !changed {
			// The renderer writes nothing for an unchanged frame, so there
			// is no flush to attribute to this key.
			p.skipped++
			continue
		}
		p.ready = append(p.ready, now.Sub(at))
		p.readyAt = append(p.readyAt, now)
	}
	p.awaiting = p.awaiting[:0]
	p.mu.Unlock()
	return v
}

func (p *probe) inject(w io.Writer, key string) error {
	p.mu.Lock()
	p.injected = append(p.injected, time.Now())
	p.mu.Unlock()
	_, err := io.WriteString(w, key)
	return err
}

// writeClock records when the program writes to the terminal.
type writeClock struct {
	mu     sync.Mutex
	writes []time.Time
	bytes  int
}

func (c *writeClock) Write(b []byte) (int, error) {
	c.mu.Lock()
	c.writes = append(c.writes, time.Now())
	c.bytes += len(b)
	c.mu.Unlock()
	return len(b), nil
}

// flushedAfter returns, for each time, the delay until the next write.
func (c *writeClock) flushedAfter(starts, readyAt []time.Time) []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Duration
	for i, s := range starts {
		j, _ := slices.BinarySearchFunc(c.writes, readyAt[i], func(a, b time.Time) int { return a.Compare(b) })
		if j < len(c.writes) {
			out = append(out, c.writes[j].Sub(s))
		}
	}
	return out
}

type stats struct {
	n                  int
	p50, p95, p99, max time.Duration
}

func summarize(d []time.Duration) stats {
	if len(d) == 0 {
		return stats{}
	}
	s := slices.Clone(d)
	slices.Sort(s)
	q := func(p float64) time.Duration { return s[min(int(p*float64(len(s))), len(s)-1)] }
	return stats{n: len(s), p50: q(0.50), p95: q(0.95), p99: q(0.99), max: s[len(s)-1]}
}

func (s stats) row(name, budget string) string {
	ms := func(d time.Duration) string {
		return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 2, 64)
	}
	return fmt.Sprintf("| %s | %d | %s | %s | %s | %s | %s |", name, s.n, ms(s.p50), ms(s.p95), ms(s.p99), ms(s.max), budget)
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func rssMB() float64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if kb, ok := strings.CutPrefix(s.Text(), "VmRSS:"); ok {
			v, _ := strconv.ParseFloat(strings.Fields(kb)[0], 64)
			return v / 1024
		}
	}
	return 0
}

type loadResult struct {
	ready, flushed, views, batches stats
	cpuPct, idleCPUPct, rss        float64
	goroutines, flushes, overflows int
	skipped                        int
	messages                       int
	keys                           int
	duration, queueWait            time.Duration
	disconnects, downloads         int
	imported                       time.Duration // S6: from start to the end of the import
}

// messageCount returns how many messages are stored.
func (d *demo) messageCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := d.pipe.rdb.QueryRowContext(context.Background(), "SELECT count(*) FROM messages").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// scenario configures one run of the harness.
type scenario struct {
	rate     float64       // live messages per second
	d        time.Duration // how long keys are typed
	keyEvery time.Duration
	idle     time.Duration // the idle phase after the traffic stops
	history  int           // the busy group's history (default 20,000)
	// key returns the n-th key typed; nil alternates up and down in the
	// message list.
	key func(n int) string
	// members types the keys in the busy group's members view, whose
	// members have names that stress rendering (S4).
	members bool
	// duringImport types keys while the history imports, until it ends
	// (S6), instead of after it.
	duringImport bool
	// downloads keeps this many media downloads waiting or running, each
	// taking a second, while another chat opens every switchEvery (S7).
	downloads   int
	switchEvery time.Duration
	// flapEvery disconnects the backend this often, for two seconds (S8).
	flapEvery time.Duration
}

// runLoad runs one session: messages at the scenario's rate for d, typing
// a key every keyEvery, then an idle phase without traffic.
func runLoad(t *testing.T, sc scenario) loadResult {
	t.Helper()
	cfg := fake.Config{
		Seed: 5, Chats: 40, MembersPerGroup: 25, HotChatMembers: 5000, HotChatHistory: cmp.Or(sc.history, 20000),
		InitialHistory: 150, HistoryChunk: 500, Rate: sc.rate, HotChatShare: 100.0 / 120.0,
		TypingRate: 0.15, AutoReceipts: true, StressNames: sc.members, FlapEvery: sc.flapEvery,
	}
	if sc.downloads > 0 {
		cfg.DownloadLatency = time.Second
	}
	began := time.Now()
	session, err := newDemo(context.Background(), cfg, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pr := &probe{inner: session.model}
	in, keys := io.Pipe()
	out := &writeClock{}
	p := tea.NewProgram(pr, tea.WithInput(in), tea.WithOutput(out), tea.WithWindowSize(200, 50), tea.WithFPS(frameRate))
	interval := bridgeInterval
	if ms, err := strconv.Atoi(os.Getenv("TARCIN_LOADTEST_BRIDGE_MS")); err == nil && ms > 0 {
		interval = time.Duration(ms) * time.Millisecond
	}
	bridge, stop, err := session.start(p.Send, interval)
	if err != nil {
		t.Fatal(err)
	}
	progDone := make(chan error, 1)
	go func() { _, err := p.Run(); progDone <- err }()

	// Wait for the initial sync and the history import, or only for the
	// busy group's first chunk if keys are typed during the import; then
	// open the busy group.
	total := cfg.HotChatHistory + cfg.InitialHistory*(cfg.Chats-1)
	wait := total
	if sc.duringImport {
		wait = cfg.HistoryChunk
	}
	deadline := time.Now().Add(2 * time.Minute)
	for session.messageCount(t) < wait {
		if time.Now().After(deadline) {
			t.Fatal("the history import did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	chats := session.chatIDs(t, 10)
	messagesBefore := session.messageCount(t)
	p.Send(chatlist.OpenMsg{ID: chats[0]})
	time.Sleep(500 * time.Millisecond)

	key := sc.key
	if key == nil {
		// Move the selection away from the newest message, then alternate
		// up and down so that every key visibly moves the selection.
		for range 5 {
			_ = pr.inject(keys, "k")
			time.Sleep(sc.keyEvery)
		}
		key = func(n int) string { return [...]string{"k", "j"}[n%2] }
	}
	if sc.members {
		_ = pr.inject(keys, ":members\r")
		time.Sleep(500 * time.Millisecond)
	}
	pr.mu.Lock()
	pr.ready, pr.readyAt, pr.skipped = nil, nil, 0
	pr.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	var background sync.WaitGroup
	if sc.downloads > 0 {
		session.download(ctx, t, &background, sc.downloads, chats)
	}
	if sc.switchEvery > 0 {
		background.Go(func() {
			tick := time.NewTicker(sc.switchEvery)
			defer tick.Stop()
			for i := 1; ; i++ {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					p.Send(chatlist.OpenMsg{ID: chats[i%len(chats)]})
				}
			}
		})
	}

	cpu0, wall0 := cpuTime(), time.Now()
	n := 0
	var imported time.Duration
	for time.Since(wall0) < sc.d {
		if err := pr.inject(keys, key(n)); err != nil {
			t.Fatal(err)
		}
		n++
		time.Sleep(sc.keyEvery)
		if sc.duringImport && n%10 == 0 && session.messageCount(t) >= total {
			imported = time.Since(began)
			break
		}
	}
	wall := time.Since(wall0)
	cpuPct := 100 * float64(cpuTime()-cpu0) / float64(wall)
	ingested := session.messageCount(t) - messagesBefore
	rss := rssMB()
	goroutines := runtime.NumGoroutine()
	cancel()
	background.Wait()

	// Idle phase: stop the traffic and measure what remains.
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second) // let the last batches drain
	queueWait := session.pipe.writer.Stats().MaxQueueWait
	cpuIdle0, idle0 := cpuTime(), time.Now()
	time.Sleep(sc.idle)
	idleCPU := 100 * float64(cpuTime()-cpuIdle0) / float64(time.Since(idle0))

	_ = pr.inject(keys, "\x11") // ctrl+q
	if err := <-progDone; err != nil {
		t.Fatal(err)
	}
	_ = keys.Close()

	pr.mu.Lock()
	defer pr.mu.Unlock()
	// Match ready samples with their injection times for flush latency.
	starts := make([]time.Time, len(pr.ready))
	for i := range pr.ready {
		starts[i] = pr.readyAt[i].Add(-pr.ready[i])
	}
	flushes, overflows := bridge.Stats()
	return loadResult{
		ready:       summarize(pr.ready),
		flushed:     summarize(out.flushedAfter(starts, pr.readyAt)),
		views:       summarize(pr.views),
		batches:     summarize(pr.batches),
		cpuPct:      cpuPct,
		idleCPUPct:  idleCPU,
		rss:         rss,
		goroutines:  goroutines,
		queueWait:   queueWait,
		flushes:     flushes,
		overflows:   overflows,
		skipped:     pr.skipped,
		messages:    ingested,
		keys:        n,
		duration:    wall,
		disconnects: pr.disconnects,
		downloads:   session.fb.Downloads(),
		imported:    imported,
	}
}

// chatIDs returns the IDs of the first n chats of the fake account; the
// first is the busy group.
func (d *demo) chatIDs(t *testing.T, n int) []domain.ChatID {
	t.Helper()
	var ids []domain.ChatID
	for _, c := range d.fb.Chats()[:n] {
		id, _, err := d.pipe.reader.ChatID(context.Background(), c.JID)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// download keeps n downloads of the chats' media waiting or running
// until ctx ends.
func (d *demo) download(ctx context.Context, t *testing.T, wg *sync.WaitGroup, n int, chats []domain.ChatID) {
	t.Helper()
	ids, err := json.Marshal(chats)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := d.pipe.rdb.QueryContext(ctx, `SELECT m.chat_id, m.wa_id FROM media JOIN messages m ON m.id = media.message_id
		WHERE m.chat_id IN (SELECT value FROM json_each(?)) ORDER BY m.id DESC`, string(ids))
	if err != nil {
		t.Fatal(err)
	}
	var media []domain.Message
	for rows.Next() {
		var m domain.Message
		if err := rows.Scan(&m.ChatID, &m.WAID); err != nil {
			t.Fatal(err)
		}
		media = append(media, m)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	if len(media) == 0 {
		t.Fatal("no media to download")
	}
	var next atomic.Int64
	for range n {
		wg.Go(func() {
			for ctx.Err() == nil {
				msg := media[int(next.Add(1)-1)%len(media)]
				_, _, _ = d.pipe.media.local(ctx, &msg)
			}
		})
	}
}

func machine() string {
	cpu, cores, mem := "unknown", runtime.NumCPU(), "unknown"
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for line := range strings.Lines(string(data)) {
			if v, ok := strings.CutPrefix(line, "model name"); ok {
				cpu = strings.TrimSpace(strings.TrimLeft(v, "\t :"))
				break
			}
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		if f := strings.Fields(strings.SplitN(string(data), "\n", 2)[0]); len(f) >= 2 {
			kb, _ := strconv.ParseFloat(f[1], 64)
			mem = fmt.Sprintf("%.1f GiB", kb/1024/1024)
		}
	}
	kernel := "unknown"
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		kernel = strings.TrimSpace(string(data))
	}
	return fmt.Sprintf("- CPU: %s (%d logical cores)\n- RAM: %s\n- Kernel: %s\n- Go: %s", cpu, cores, mem, kernel, runtime.Version())
}

func TestLoad(t *testing.T) {
	if os.Getenv("TARCIN_LOADTEST") != "1" {
		t.Skip("set TARCIN_LOADTEST=1 to run the load harness (make loadtest)")
	}
	seconds, _ := strconv.Atoi(os.Getenv("TARCIN_LOADTEST_SECONDS"))
	if seconds <= 0 {
		seconds = 60
	}
	d := time.Duration(seconds) * time.Second
	idle := 10 * time.Second

	every := 50 * time.Millisecond
	quiet := runLoad(t, scenario{d: d / 3, keyEvery: every, idle: idle})
	busy := runLoad(t, scenario{rate: 120, d: d, keyEvery: every, idle: idle})
	// S4: a hundred pages down (ctrl+f) through the 5,000 members, then a
	// hundred back up (ctrl+b).
	pages := func(n int) string { return [...]string{"\x06", "\x02"}[n/100%2] }
	members := runLoad(t, scenario{rate: 20, d: d / 3, keyEvery: every, members: true, key: pages})
	imports := runLoad(t, scenario{rate: 20, d: d, keyEvery: every, history: 50000, duringImport: true})
	media := runLoad(t, scenario{rate: 20, d: d / 3, keyEvery: every, downloads: 4, switchEvery: 500 * time.Millisecond})
	// S8 runs for 2 minutes by default, and long enough for two flaps.
	flaps := runLoad(t, scenario{rate: 120, d: max(2*d, 25*time.Second), keyEvery: every, flapEvery: 10 * time.Second})

	var b strings.Builder
	fmt.Fprintf(&b, "# Load test (scenarios S4–S8) — %s\n\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&b, "%s\n- Terminal: none; the program writes to an in-process writer at 200x50, %d fps\n", machine(), frameRate)
	fmt.Fprintf(&b, "- Data: 40 chats, a 5,000-member group with 20,000 history messages (open), fake backend seed 5;\n  every event goes through the writer into SQLite and back to the UI\n\n")
	fmt.Fprintf(&b, "Latencies in milliseconds. *Ready* is from writing a key to the end of the View that follows its Update; *flushed* adds the wait for Bubble Tea's frame ticker until the frame is written.\n\n")
	b.WriteString("## S5: a busy group\n\n")
	b.WriteString("| Measurement | Samples | p50 | p95 | p99 | max | Budget |\n|---|---|---|---|---|---|---|\n")
	fmt.Fprintf(&b, "%s\n", quiet.ready.row("Key → ready, idle backend", "B3a ≤ 2 ms p95"))
	fmt.Fprintf(&b, "%s\n", quiet.flushed.row("Key → flushed, idle backend", "B3b ≤ 25 ms p95"))
	fmt.Fprintf(&b, "%s\n", busy.ready.row("Key → ready, 120 msg/s", "B4 ≤ 2 ms p95"))
	fmt.Fprintf(&b, "%s\n", busy.flushed.row("Key → flushed, 120 msg/s", "B4 ≤ 33 ms p95"))
	fmt.Fprintf(&b, "%s\n", busy.views.row("View, 120 msg/s", "—"))
	fmt.Fprintf(&b, "%s\n\n", busy.batches.row("Bridge batch Update, 120 msg/s", "—"))
	fmt.Fprintf(&b, "| Resource (120 msg/s phase) | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Duration | %.0f s |\n| Keys typed | %d |\n| Messages ingested | %d (%.0f/s) |\n", busy.duration.Seconds(), busy.keys, busy.messages, float64(busy.messages)/busy.duration.Seconds())
	fmt.Fprintf(&b, "| Bridge flushes / overflows | %d / %d |\n| CPU (whole process, incl. fake backend) | %.1f%% of one core |\n", busy.flushes, busy.overflows, busy.cpuPct)
	fmt.Fprintf(&b, "| RSS (whole test process, SQLite store) | %.0f MB |\n| Goroutines | %d |\n", busy.rss, busy.goroutines)
	fmt.Fprintf(&b, "| Longest wait of a submission in the writer's queues (B9: ≤ 100 ms p99) | %.1f ms |\n", float64(busy.queueWait.Microseconds())/1000)
	fmt.Fprintf(&b, "| Idle CPU after traffic stops (%.0f s) | %.2f%% of one core |\n\n", idle.Seconds(), busy.idleCPUPct)

	b.WriteString("## S4, S6, S7 and S8\n\n")
	b.WriteString("- S4: the members view of the 5,000-member group, whose members have long, emoji and right-to-left names; a hundred pages down and back up, at 20 msg/s\n")
	fmt.Fprintf(&b, "- S6: keys typed while a history of %d messages (50,000 in the busy group) imports, at 20 msg/s\n", 50000+150*39)
	b.WriteString("- S7: another chat opens every 500 ms while four media downloads of a second each wait or run, at 20 msg/s\n")
	fmt.Fprintf(&b, "- S8: the backend disconnects every 10 s for 2 s, for %.0f s, at 120 msg/s; messages that arrive offline come as a burst on reconnect\n\n", flaps.duration.Seconds())
	b.WriteString("| Measurement | Samples | p50 | p95 | p99 | max | Budget |\n|---|---|---|---|---|---|---|\n")
	for _, r := range []struct {
		name string
		res  loadResult
	}{{"S4 members", members}, {"S6 import", imports}, {"S7 media", media}, {"S8 flaps", flaps}} {
		fmt.Fprintf(&b, "%s\n", r.res.ready.row("Key → ready, "+r.name, "≤ 2 ms p95"))
		fmt.Fprintf(&b, "%s\n", r.res.flushed.row("Key → flushed, "+r.name, "≤ 33 ms p95"))
	}
	b.WriteString("\n| Scenario | Keys | Messages ingested | Longest writer queue wait | CPU | RSS | Other |\n|---|---|---|---|---|---|---|\n")
	row := func(name string, r loadResult, other string) {
		fmt.Fprintf(&b, "| %s | %d | %d | %.1f ms | %.1f%% | %.0f MB | %s |\n", name, r.keys, r.messages, float64(r.queueWait.Microseconds())/1000, r.cpuPct, r.rss, other)
	}
	row("S4", members, "—")
	row("S6", imports, fmt.Sprintf("import finished %.1f s after start", imports.imported.Seconds()))
	row("S7", media, fmt.Sprintf("downloads finished: %d", media.downloads))
	row("S8", flaps, fmt.Sprintf("disconnections shown: %d", flaps.disconnects))
	report := b.String()
	t.Log("\n" + report)
	if path := os.Getenv("TARCIN_LOADTEST_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(report), 0o600); err != nil { //nolint:gosec // path chosen by the developer running the harness
			t.Fatal(err)
		}
	}
	if busy.ready.n < busy.keys*9/10 {
		t.Errorf("only %d of %d keys produced a measured frame (%d left the frame unchanged)", busy.ready.n, busy.keys, busy.skipped)
	}
	if imports.imported == 0 {
		t.Error("S6: the history import did not finish while keys were typed")
	}
	if media.downloads == 0 {
		t.Error("S7: no media was downloaded")
	}
	if flaps.disconnects == 0 {
		t.Error("S8: the UI was not told about a disconnection")
	}
}
