// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/logging"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
)

// TestLogsCarryNoContent checks the log redaction end to end: canary
// text, a canary name and canary addresses go through the paths that
// log (storing, failed sends, reads, a full disk and its recovery,
// shutdown), and none of them may reach the log. The adapter's
// and whatsmeow's lines are covered in internal/wa.
func TestLogsCarryNoContent(t *testing.T) {
	const (
		body    = "CANARY-BODY-c4n4ry"
		name    = "CANARY-NAME-Zeynep"
		pnUser  = "905550001122"
		lidUser = "246813579024680"
	)
	pn, lid := domain.JID(pnUser+"@s.whatsapp.net"), domain.JID(lidUser+"@lid")
	out := &lockedBuffer{}
	fb := fake.New(fake.Config{Seed: 21, Chats: 3, SendFailRate: 1, Tick: time.Millisecond})
	me := fb.Me()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{
		MeLID: me.LID, MePN: me.PN, Backend: fb, ReadReceipts: true,
		Log: slog.New(logging.NewHandler(out, slog.LevelDebug)),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newRecordingUI()
	if err := p.start(r); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	waitUntil(t, "the initial sync", func() bool {
		chats, _ := p.reader.Chats(t.Context())
		return len(chats) == 3
	})
	chats, _ := p.reader.Chats(t.Context())
	chat := chats[0]
	canary := func(waid string) []domain.Event {
		return []domain.Event{
			domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
				ChatJID: chat.JID, SenderJID: lid, SenderAlt: pn, PushName: name, WAID: domain.WAMessageID(waid),
				Timestamp: time.Now(), Kind: domain.KindText, Body: body + strings.Repeat(" padding", 500),
			}},
			domain.ContactUpdated{JID: lid, PushName: name},
		}
	}
	if err := p.Deliver(t.Context(), canary("C1")); err != nil {
		t.Fatal(err)
	}

	s := newSender(fb, p)
	p.send(s, fb)(ui.Outgoing{Chat: chat.ID, Text: body}) // fails: SendFailRate 1
	waitUntil(t, "the failed send", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.ContainsFunc(r.notices, func(n string) bool { return strings.HasPrefix(n, "Message not sent") })
	})
	_ = p.read(chat.ID)()

	// A full disk, then space again.
	var pages int
	if err := p.wdb.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := p.wdb.ExecContext(t.Context(), fmt.Sprintf("PRAGMA max_page_count = %d", pages)); err != nil {
		t.Fatal(err)
	}
	full := false
	for i := 0; i < 1000 && !full; i++ {
		full = p.Deliver(t.Context(), canary(fmt.Sprintf("F%d", i))) != nil
	}
	if !full {
		t.Fatal("the database did not fill up")
	}
	if _, err := p.wdb.ExecContext(t.Context(), "PRAGMA max_page_count = 1073741823"); err != nil {
		t.Fatal(err)
	}
	if err := p.Deliver(t.Context(), canary("C2")); err != nil {
		t.Fatal(err)
	}
	cancel()
	s.close()
	if err := p.close(context.Background()); err != nil {
		t.Fatal(err)
	}

	log := out.text()
	for _, leak := range []string{body, name, pnUser, lidUser} {
		if strings.Contains(log, leak) {
			t.Fatalf("the log leaks %q:\n%s", leak, log)
		}
	}
	for _, want := range []string{"message not sent", "stopped accepting writes", "accepts writes again", "message store closed"} {
		if !strings.Contains(log, want) {
			t.Fatalf("the log lacks %q:\n%s", want, log)
		}
	}
}
