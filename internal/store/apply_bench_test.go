// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// chunk returns n history messages of a group from 50 members, a tenth
// of them with media, starting at message number from.
func chunk(from, n int) []domain.Event {
	events := make([]domain.Event, 0, n)
	for i := from; i < from+n; i++ {
		m := domain.IncomingMessage{
			ChatJID: group, SenderJID: domain.JID(fmt.Sprintf("%d@lid", 1000+i%50)), PushName: fmt.Sprintf("Member %d", i%50),
			WAID: domain.WAMessageID(fmt.Sprintf("W%08d", i)), Timestamp: epoch.Add(time.Duration(i) * time.Second),
			Kind: domain.KindText, Body: fmt.Sprintf("message number %d about the meeting tomorrow", i),
		}
		if i%10 == 0 {
			m.Kind, m.Raw = domain.KindImage, []byte("raw")
			m.Media = &domain.Media{MIME: "image/jpeg", Size: 120_000, Width: 1280, Height: 960}
		}
		events = append(events, domain.MessageReceived{Source: domain.SourceHistory, Message: m})
	}
	return events
}

// BenchmarkApplyHistoryChunk applies history chunks of 500 messages of
// one group, each in its own transaction, as the writer does.
func BenchmarkApplyHistoryChunk(b *testing.B) {
	db, _ := readFixture(b)
	a := NewEventApplier(ApplierOptions{MeLID: meLID, MePN: mePN})
	const size = 500
	n := 0
	for b.Loop() {
		tx, err := db.BeginTx(b.Context(), nil)
		if err != nil {
			b.Fatal(err)
		}
		if err := a.Apply(b.Context(), tx, chunk(n, size), &Changes{}); err != nil {
			b.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
		n += size
	}
	b.ReportMetric(float64(n)/b.Elapsed().Seconds(), "msg/s")
}

// BenchmarkApplyLiveMessage applies one live message per transaction, as
// the writer does for a message that arrives alone.
func BenchmarkApplyLiveMessage(b *testing.B) {
	db, _ := readFixture(b)
	a := NewEventApplier(ApplierOptions{MeLID: meLID, MePN: mePN})
	n := 0
	for b.Loop() {
		ev := chunk(n, 1)[0].(domain.MessageReceived)
		ev.Source = domain.SourceLive
		tx, err := db.BeginTx(b.Context(), nil)
		if err != nil {
			b.Fatal(err)
		}
		if err := a.Apply(b.Context(), tx, []domain.Event{ev}, &Changes{}); err != nil {
			b.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
		n++
	}
}
