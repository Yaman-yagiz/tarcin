// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// TestCrashHelperProcess writes messages for TestCrashKeepsEveryCommit
// until it is killed: live messages from several submitters at once and
// history chunks, printing the ID of each message once its commit
// returned.
func TestCrashHelperProcess(t *testing.T) {
	path := os.Getenv("TARCIN_CRASH_HELPER")
	if path == "" {
		t.Skip("helper process only")
	}
	ctx := context.Background()
	db, err := Open(ctx, path, RoleWriter)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	if _, _, err := Migrate(ctx, db, Migrations(), MigrateOptions{}); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	w, err := NewWriter(db, WriterOptions{Applier: NewEventApplier(ApplierOptions{})})
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	out := make(chan string, 1024)
	msg := func(chat domain.JID, waid string, src domain.Source) domain.Event {
		return domain.MessageReceived{Source: src, Message: domain.IncomingMessage{
			ChatJID: chat, SenderJID: "9@lid", WAID: domain.WAMessageID(waid), Timestamp: time.Now(),
			Kind: domain.KindText, Body: "crash test " + waid + strings.Repeat(" padding", 20),
		}}
	}
	for s := range 4 {
		go func() {
			for i := 0; ; i++ {
				waid := fmt.Sprintf("L%d-%d", s, i)
				if w.SubmitLive(ctx, []domain.Event{msg("g@g.us", waid, domain.SourceLive)}) == nil {
					out <- waid
				}
			}
		}()
	}
	go func() {
		for i := 0; ; i++ {
			chunk := make([]domain.Event, 200)
			ids := make([]string, len(chunk))
			for j := range chunk {
				ids[j] = fmt.Sprintf("H%d-%d", i, j)
				chunk[j] = msg("h@g.us", ids[j], domain.SourceHistory)
			}
			if w.SubmitBulk(ctx, chunk) == nil {
				for _, id := range ids {
					out <- id
				}
			}
		}
	}()
	fmt.Println("ready")
	stdout := bufio.NewWriter(os.Stdout)
	for id := range out {
		fmt.Fprintln(stdout, id)
		if len(out) == 0 {
			_ = stdout.Flush()
		}
	}
}

// TestCrashKeepsEveryCommit is scenario S10: killing the process with
// SIGKILL during a write burst loses no committed message and leaves an
// intact database. With WAL and synchronous=NORMAL only a power loss,
// not a crash, may undo the last commits (docs/architecture/data-model.md §5).
func TestCrashKeepsEveryCommit(t *testing.T) {
	rounds := 3
	if testing.Short() {
		rounds = 1
	}
	path := filepath.Join(t.TempDir(), "tarcin.db")
	committed := map[string]bool{}
	for round := range rounds {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCrashHelperProcess$") //nolint:gosec // the test binary itself
		cmd.Env = append(os.Environ(), "TARCIN_CRASH_HELPER="+path)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		lines := bufio.NewScanner(stdout)
		if !lines.Scan() || lines.Text() != "ready" {
			_ = cmd.Process.Kill()
			t.Fatalf("helper: %q", lines.Text())
		}
		// Let it write for a while, killing it at a different point each
		// round, then read what it reported before it died.
		want := 2000 + round*1500
		n := 0
		for n < want && lines.Scan() {
			committed[lines.Text()] = true
			n++
		}
		if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		for lines.Scan() {
			committed[lines.Text()] = true
		}
		_ = cmd.Wait()
		if n < want {
			t.Fatalf("round %d: the helper stopped after %d commits", round, n)
		}
		checkSurvivors(t, path, committed)
	}
}

func checkSurvivors(t *testing.T, path string, committed map[string]bool) {
	t.Helper()
	db, err := Open(t.Context(), path, RoleWriter) // recovers the WAL
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var check string
	if err := db.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q, %v", check, err)
	}
	rows, err := db.QueryContext(t.Context(), "SELECT wa_id FROM messages")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	stored := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		stored[id] = true
	}
	for id := range committed {
		if !stored[id] {
			t.Fatalf("committed message %s was lost (%d committed, %d stored)", id, len(committed), len(stored))
		}
	}
	var fk int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_foreign_key_check").Scan(&fk); err != nil || fk != 0 {
		t.Fatalf("foreign_key_check: %d, %v", fk, err)
	}
}
