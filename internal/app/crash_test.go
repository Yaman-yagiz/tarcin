// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/store"
)

// none returns an empty slice the linters cannot see is empty.
func none() []int { return nil }

// panicky panics on the key "p" in Update, on "v" in View, and returns a
// command that panics on "c".
type panicky struct{ viewPanics bool }

func (m *panicky) Init() tea.Cmd { return nil }

func (m *panicky) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	switch {
	case !ok:
	case k.String() == "p":
		panic(errors.New("secret message body"))
	case k.String() == "v":
		m.viewPanics = true
	case k.String() == "c":
		return m, tea.Batch(nil, func() tea.Msg { return none()[3] })
	}
	return m, nil
}

func (m *panicky) View() tea.View {
	if m.viewPanics {
		panic("secret in view")
	}
	return tea.NewView("ok")
}

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestGuard() (*guard, *logBuffer) {
	var out logBuffer
	return newGuard(slog.New(slog.NewTextHandler(&out, nil))), &out
}

// runGuarded runs the panicky model under the guard in a real program
// and types key into it.
func runGuarded(t *testing.T, key string) (*guard, string) {
	t.Helper()
	g, log := newTestGuard()
	in, keys := io.Pipe()
	p := tea.NewProgram(g.model(&panicky{}), tea.WithInput(in), tea.WithOutput(io.Discard), tea.WithWindowSize(40, 10))
	g.attach(p.Send)
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	if _, err := io.WriteString(keys, key); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the program ended with %v; a guarded panic quits it", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not stop")
	}
	_ = keys.Close()
	return g, log.String()
}

func TestGuardStopsTheProgram(t *testing.T) {
	for _, tc := range []struct{ key, where, panic string }{
		{"p", "ui.update", "*errors.errorString"},
		{"v", "ui.view", "string"},
		{"c", "ui.command", "runtime error: index out of range [3] with length 0"},
	} {
		g, log := runGuarded(t, tc.key)
		if !g.crashed.Load() {
			t.Fatalf("%s: not reported as a crash", tc.where)
		}
		if !strings.Contains(log, "where="+tc.where) || !strings.Contains(log, tc.panic) || !strings.Contains(log, "crash_test.go:") {
			t.Fatalf("%s: log %q", tc.where, log)
		}
		if strings.Contains(log, "secret") {
			t.Fatalf("%s: the panic's value reached the log: %q", tc.where, log)
		}
	}
}

func TestGuardProtectsGoroutines(t *testing.T) {
	g, log := newTestGuard()
	got := make(chan tea.Msg, 1)
	g.attach(func(msg tea.Msg) { got <- msg })
	go func() {
		defer g.protect("sender")
		panic("secret")
	}()
	select {
	case msg := <-got:
		if _, ok := msg.(crashMsg); !ok {
			t.Fatalf("sent %T", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the program was not told")
	}
	if !g.crashed.Load() || !strings.Contains(log.String(), "where=sender") || strings.Contains(log.String(), "secret") {
		t.Fatalf("crashed %v, log %q", g.crashed.Load(), log.String())
	}
	m := g.model(&panicky{})
	if _, cmd := m.Update(tea.KeyPressMsg{}); cmd == nil || cmd() != tea.Quit() {
		t.Fatal("after a crash, every message quits")
	}
}

type panickyApplier struct{}

func (panickyApplier) Apply(context.Context, *sql.Tx, []domain.Event, *store.Changes) error {
	_ = none()[0] // a runtime error
	return nil
}

func TestGuardedApplierFailsTheSubmission(t *testing.T) {
	g, log := newTestGuard()
	a := &guardedApplier{inner: panickyApplier{}, g: g}
	if err := a.Apply(t.Context(), nil, nil, nil); !errors.Is(err, errApplierPanic) {
		t.Fatalf("err = %v", err)
	}
	if g.crashed.Load() || !strings.Contains(log.String(), "where=store.apply") {
		t.Fatalf("crashed %v, log %q; the submission fails, the program goes on", g.crashed.Load(), log.String())
	}
	if err := a.Maintain(t.Context(), nil); err != nil {
		t.Fatalf("Maintain = %v for an applier without upkeep", err)
	}
}
