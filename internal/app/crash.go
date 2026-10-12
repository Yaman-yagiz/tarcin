// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"runtime/trace"
	"strings"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/store"
)

// ErrCrashed is returned by a session that stopped after a panic.
var ErrCrashed = errors.New("stopped after an internal error")

// errApplierPanic fails a submission whose events made the applier panic.
var errApplierPanic = errors.New("applying the events failed with an internal error")

// guard turns a panic into a report in the log and a clean stop
// (security.md §5): Bubble Tea's normal shutdown restores the terminal,
// and the caller tells the user where the report is. A report holds the
// panic's type, or a runtime error's text, and where it happened; never
// the panic's value, which could hold message content.
type guard struct {
	log     *slog.Logger
	crashed atomic.Bool

	mu   sync.Mutex
	stop func() // asks the program to quit; nil before it runs
	last string // the latest report, for the demo, which logs nothing
}

func newGuard(log *slog.Logger) *guard {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &guard{log: log}
}

// crashMsg tells the guarded model that a goroutine crashed.
type crashMsg struct{}

// attach makes crashes stop the program send belongs to.
func (g *guard) attach(send func(tea.Msg)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stop = func() { send(crashMsg{}) }
}

// report logs a recovered panic r.
func (g *guard) report(where string, r any) {
	what := fmt.Sprintf("%T", r)
	if re, ok := r.(runtime.Error); ok {
		what = re.Error()
	}
	stack := stackOf(3)
	g.log.Error("internal error", "where", where, "panic", what, "stack", stack)
	g.mu.Lock()
	g.last = fmt.Sprintf("%s: %s\n%s", where, what, strings.ReplaceAll(stack, " < ", "\n"))
	g.mu.Unlock()
}

// crash reports a panic and stops the program. The stop is asked for
// from another goroutine: the panic may be in the program's own loop,
// which would never take the message.
func (g *guard) crash(where string, r any) {
	g.report(where, r)
	g.crashed.Store(true)
	g.mu.Lock()
	stop := g.stop
	g.mu.Unlock()
	if stop != nil {
		go stop()
	}
}

// protect is deferred at the top of a goroutine: a panic in it stops the
// program instead of the process, which would leave the terminal raw.
func (g *guard) protect(where string) {
	if r := recover(); r != nil {
		g.crash(where, r)
	}
}

// stackOf describes the calling goroutine's stack from skip frames up, as
// function and file:line, newest first.
func stackOf(skip int) string {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(skip, pcs)])
	var b strings.Builder
	for {
		f, more := frames.Next()
		if f.Function != "" && !strings.HasPrefix(f.Function, "runtime.") {
			if b.Len() > 0 {
				b.WriteString(" < ")
			}
			fmt.Fprintf(&b, "%s (%s:%d)", f.Function, filepath.Base(f.File), f.Line)
		}
		if !more {
			return b.String()
		}
	}
}

// model wraps the root model: a panic in Update, View or a command it
// returned stops the program.
func (g *guard) model(inner tea.Model) tea.Model { return &guardedModel{inner: inner, g: g} }

type guardedModel struct {
	inner tea.Model
	g     *guard
}

func (m *guardedModel) Init() (cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			m.g.crash("ui.init", r)
			cmd = tea.Quit
		}
	}()
	return m.g.cmd(m.inner.Init())
}

func (m *guardedModel) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	if _, ok := msg.(crashMsg); ok || m.g.crashed.Load() {
		return m, tea.Quit
	}
	defer trace.StartRegion(context.Background(), "ui.update").End()
	defer func() {
		if r := recover(); r != nil {
			m.g.crash("ui.update", r)
			model, cmd = m, tea.Quit
		}
	}()
	m.inner, cmd = m.inner.Update(msg)
	return m, m.g.cmd(cmd)
}

func (m *guardedModel) View() (v tea.View) {
	if m.g.crashed.Load() {
		return tea.View{}
	}
	defer trace.StartRegion(context.Background(), "ui.view").End()
	defer func() {
		if r := recover(); r != nil {
			m.g.crash("ui.view", r)
			v = tea.View{}
		}
	}()
	return m.inner.View()
}

// cmd guards a command, and the commands of a batch it returns.
func (g *guard) cmd(c tea.Cmd) tea.Cmd {
	if c == nil {
		return nil
	}
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				g.crash("ui.command", r)
				msg = crashMsg{}
			}
		}()
		msg = c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			out := make(tea.BatchMsg, len(batch))
			for i, c := range batch {
				out[i] = g.cmd(c)
			}
			return out
		}
		return msg
	}
}

// guardedApplier fails a submission whose events make the applier panic,
// instead of the writer: one malformed event must not stop the client.
type guardedApplier struct {
	inner store.Applier
	g     *guard
}

func (a *guardedApplier) Apply(ctx context.Context, tx *sql.Tx, events []domain.Event, ch *store.Changes) (err error) {
	defer func() {
		if r := recover(); r != nil {
			a.g.report("store.apply", r)
			err = errApplierPanic
		}
	}()
	return a.inner.Apply(ctx, tx, events, ch)
}

func (a *guardedApplier) Maintain(ctx context.Context, tx *sql.Tx) (err error) {
	m, ok := a.inner.(store.Maintainer)
	if !ok {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			a.g.report("store.maintain", r)
			err = errApplierPanic
		}
	}()
	return m.Maintain(ctx, tx)
}
