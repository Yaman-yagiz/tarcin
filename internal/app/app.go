// SPDX-License-Identifier: GPL-3.0-or-later

// Package app is the composition root: it wires configuration, storage,
// the protocol backend and the UI together and runs them. Only cmd/tarcin
// imports it (enforced by internal/archtest).
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/paths"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
)

// frameRate caps renders per second. Bubble Tea flushes frames on a
// ticker, so the frame interval, not rendering (well under a millisecond),
// dominates key-to-screen latency; 60 fps keeps it below 17 ms
// (docs/architecture/performance.md, budgets B3 and B4).
const frameRate = 60

// bridgeInterval is how long new messages are collected before they are
// shown. Key presses are not delayed by it.
const bridgeInterval = ui.DefaultFlushInterval

// DumpKeys runs the key-dump screen until the user quits.
func DumpKeys(ctx context.Context, in io.Reader, out io.Writer) error {
	p := tea.NewProgram(keymap.NewDumpModel(), tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out))
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("dump keys: %w", err)
	}
	return nil
}

// DemoOptions configure the demo.
type DemoOptions struct {
	Seed uint64
	Rate float64 // live messages per second
	// Link starts the demo as a device that is not linked yet, to show
	// the link screen.
	Link bool
	// ConfigPath is the config file whose theme and keys the demo uses
	// (default: the XDG config file).
	ConfigPath string
	// Extra program options, used by tests (e.g. a fixed window size).
	ProgramOptions []tea.ProgramOption
}

// demoConfig is a busy account: forty chats with history, one group of
// two thousand members with a hundred thousand messages that receives a
// large share of the live traffic, typing, duplicate deliveries and
// receipts for sent messages. The history arrives as a history sync
// would: the chats fill in while the UI stays usable (scenario S6).
func demoConfig(o DemoOptions) fake.Config {
	return fake.Config{
		Seed:            o.Seed,
		Chats:           40,
		MembersPerGroup: 25,
		HotChatMembers:  2000,
		HotChatShare:    0.4,
		InitialHistory:  150,
		HotChatHistory:  100_000,
		HistoryChunk:    500,
		OnDemandHistory: 200,
		Rate:            o.Rate,
		TypingRate:      0.15,
		DuplicateRate:   0.02,
		AutoReceipts:    true,
		SendLatency:     300 * time.Millisecond,
		Link:            o.Link,
	}
}

// RunDemo runs the UI against the fake backend and a real message
// database in a temporary directory: no account and no network are
// involved, but every event takes the path of a real session, through
// the writer and SQLite to the UI.
func RunDemo(ctx context.Context, in io.Reader, out io.Writer, o DemoOptions) error {
	if o.Rate < 0 {
		return errors.New("demo: the message rate must not be negative")
	}
	set, err := loadSettings(o.ConfigPath)
	if err != nil {
		return err
	}
	dir, err := demoDir()
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	d, err := newDemo(ctx, demoConfig(o), set, dir)
	if err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	if err := d.run(ctx, in, out, o.ProgramOptions); err != nil {
		if errors.Is(err, ErrCrashed) {
			// The demo logs nothing; its data is made up, so the report
			// is shown.
			d.pipe.guard.mu.Lock()
			defer d.pipe.guard.mu.Unlock()
			return fmt.Errorf("demo: %w at %s", err, d.pipe.guard.last)
		}
		return fmt.Errorf("demo: %w", err)
	}
	return nil
}

// demoDir creates a private directory for the demo database, in the cache
// directory when it can be resolved (it may be large; /tmp is often in
// memory).
func demoDir() (string, error) {
	base := os.TempDir()
	if p, err := paths.Default(paths.DefaultProfile); err == nil {
		if err := os.MkdirAll(p.Cache, 0o700); err == nil {
			base = p.Cache
		}
	}
	return os.MkdirTemp(base, "demo-")
}

// demo is a session with the fake backend.
type demo struct {
	*session
	fb *fake.Backend
}

func newDemo(ctx context.Context, cfg fake.Config, set *settings, dir string) (*demo, error) {
	fb := fake.New(cfg)
	me := fb.Me()
	pipe, err := openPipeline(ctx, filepath.Join(dir, "tarcin.db"), pipelineOptions{
		MeLID: me.LID, MePN: me.PN, Backend: fb, ReadReceipts: true, Typing: true, Presence: true,
		Media: mediaFromConfig(set, filepath.Join(dir, "media")),
	})
	if err != nil {
		return nil, err
	}
	welcome := ""
	if cfg.Link {
		welcome = dir
	}
	return &demo{session: newSession(fb, pipe, set, welcome), fb: fb}, nil
}

// session is a backend, the pipeline and the UI model wired together.
type session struct {
	b      backend.Backend
	pipe   *pipeline
	sender *sender
	model  *ui.Model
	retry  chan struct{}
	// begin, if not nil, is closed when the user has accepted the
	// first-run notice; the backend starts only then.
	begin chan struct{}
	// bridge is set once the session starts, for the debug pane.
	bridge atomic.Pointer[ui.Bridge]
}

// newSession wires b and pipe to a new UI model with the theme and keys
// of set (nil: the defaults). With a dataDir, the UI opens on the
// first-run notice, which names it, and the backend starts once the user
// accepts it.
func newSession(b backend.Backend, pipe *pipeline, set *settings, dataDir string) *session {
	s := &session{b: b, pipe: pipe, sender: newSender(b, pipe), retry: make(chan struct{}, 1)}
	link := linkOptions(b, s.retry)
	if link != nil && dataDir != "" {
		s.begin = make(chan struct{})
		var once sync.Once
		link.Begin = func() tea.Cmd {
			once.Do(func() { close(s.begin) })
			return nil
		}
		link.DataDir = dataDir
	}
	if set == nil {
		set = &settings{}
	}
	s.model = ui.New(ui.Options{
		Theme: set.theme, Keymap: set.keymap, History: pipe.reader, Members: pipe.reader, Search: pipe.reader, Older: pipe.older(), React: pipe.react(), Edit: pipe.edit(), Delete: pipe.revoke(), Mentionable: pipe.mentionable, Typing: pipe.typing(), Presence: pipe.presence(), Notify: set.notify, NotifyFocused: set.cfg.Notify.When == "other-chats", ChatSetting: pipe.chatSetting(), DeleteLocal: pipe.deleteLocal(),
		Metrics: pipe.metrics(func() (int, int) {
			if b := s.bridge.Load(); b != nil {
				return b.Stats()
			}
			return 0, 0
		}),
		OpenMedia: pipe.openMedia(), SaveMedia: pipe.saveMedia(), Attach: pipe.attach(s.sender, b), Names: pipe.names, Send: pipe.send(s.sender, b), Forward: pipe.forward(s.sender, b), Retry: pipe.retry(s.sender), Read: pipe.read, Attention: pipe.attention,
		Link: link, Reconnect: reconnectOption(b), DraftDir: set.drafts,
	})
	return s
}

// run runs the UI program until the user quits, and then shuts the
// session down.
func (s *session) run(ctx context.Context, in io.Reader, out io.Writer, extra []tea.ProgramOption) error {
	opts := append([]tea.ProgramOption{
		tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithFPS(frameRate),
	}, extra...)
	p := tea.NewProgram(s.pipe.guard.model(s.model), opts...)
	_, stop, err := s.start(p.Send, bridgeInterval)
	if err != nil {
		return err
	}
	_, err = p.Run()
	serr := stop()
	switch {
	case s.pipe.guard.crashed.Load():
		return ErrCrashed
	case err != nil && !errors.Is(err, tea.ErrProgramKilled) && !errors.Is(err, context.Canceled):
		return err
	case serr != nil:
		return fmt.Errorf("backend: %w", serr)
	}
	return nil
}

// start connects the pipeline to a bridge into the program and runs the
// backend. The returned function stops the backend, drains the writer
// and closes everything.
func (s *session) start(send func(tea.Msg), interval time.Duration) (bridge *ui.Bridge, stop func() error, err error) {
	s.pipe.guard.attach(send)
	bridge = ui.NewBridge(send, ui.BridgeOptions{FlushInterval: interval})
	s.bridge.Store(bridge)
	if err := s.pipe.start(bridge); err != nil {
		bridge.Close()
		_ = s.pipe.close(context.Background())
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.pipe.guard.crash("backend", r)
				done <- ErrCrashed
			}
		}()
		if s.begin != nil {
			select {
			case <-s.begin:
			case <-ctx.Done():
				done <- nil
				return
			}
		}
		done <- runBackend(ctx, s.b, s.pipe, s.retry)
	}()
	return bridge, func() error {
		s.pipe.offline() // before the connection goes
		cancel()
		berr := <-done
		s.sender.close() // before the writer: queued messages are stored as failed
		cctx, ccancel := context.WithTimeout(context.Background(), drainTimeout)
		defer ccancel()
		perr := s.pipe.close(cctx)
		bridge.Close()
		return errors.Join(berr, perr)
	}, nil
}

// drainTimeout bounds the writer drain at shutdown
// (docs/architecture/event-pipeline.md §4).
const drainTimeout = 5 * time.Second
