// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	tstore "github.com/Yaman-yagiz/tarcin/internal/store"
)

// TestNormalizedEventsFitTheStore applies normalised events with the real
// writer and reads them back as the UI does.
func TestNormalizedEventsFitTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tarcin.db")
	db, err := tstore.Open(t.Context(), path, tstore.RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := tstore.Migrate(t.Context(), db, tstore.Migrations(), tstore.MigrateOptions{}); err != nil {
		t.Fatal(err)
	}
	w, err := tstore.NewWriter(db, tstore.WriterOptions{Applier: tstore.NewEventApplier(tstore.ApplierOptions{})})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close(context.Background()) }()
	a := testAdapter(t)
	img := groupMsg(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("kedi"), FileLength: proto.Uint64(4096)}})
	img.Info.ID = "IMG"
	edit := groupMsg(&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		Key: &waCommon.MessageKey{ID: proto.String("TXT")}, EditedMessage: &waE2E.Message{Conversation: proto.String("v2")}}})
	edit.Info.Timestamp = ts.Add(time.Minute)
	txt := groupMsg(&waE2E.Message{Conversation: proto.String("v1")})
	txt.Info.ID = "TXT"
	for _, evt := range []any{img, edit, txt} {
		if !a.handle(t.Context(), w, evt) {
			t.Fatalf("%T was not stored", evt)
		}
	}
	rdb, err := tstore.Open(t.Context(), path, tstore.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	r := tstore.NewReader(rdb)
	chat, ok, err := r.ChatID(t.Context(), "120363001@g.us")
	if err != nil || !ok {
		t.Fatalf("chat: %v, %v", ok, err)
	}
	msgs, err := r.Latest(t.Context(), chat, 10)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
	byID := map[domain.WAMessageID]domain.Message{}
	for _, m := range msgs {
		byID[m.WAID] = m
	}
	if m := byID["IMG"]; m.Kind != domain.KindImage || m.Body != "kedi" || m.Media == nil || m.Media.Size != 4096 {
		t.Fatalf("image = %+v", m)
	}
	if m := byID["TXT"]; m.Body != "v2" || m.EditedAt.IsZero() {
		t.Fatalf("an edit that arrived first must apply when its message comes: %+v", m)
	}
	var raw int
	if err := rdb.QueryRowContext(t.Context(), "SELECT count(*) FROM messages WHERE raw IS NOT NULL").Scan(&raw); err != nil || raw != 1 {
		t.Fatalf("messages with raw = %d, %v; only the image keeps it", raw, err)
	}
}
