// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	tstore "github.com/Yaman-yagiz/tarcin/internal/store"
)

// testSync is a history sync with mappings, push names, a group and a
// direct chat of 1,200 messages.
// tsSec is ts in Unix seconds.
const tsSec uint64 = 1_760_000_000

func testSync() *waHistorySync.HistorySync {
	webMsg := func(chat, participant string, id uint64, text string) *waHistorySync.HistorySyncMsg {
		key := &waCommon.MessageKey{RemoteJID: proto.String(chat), ID: proto.String(fmt.Sprintf("H%04d", id)), FromMe: proto.Bool(false)}
		if participant != "" {
			key.Participant = proto.String(participant)
		}
		return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{Key: key,
			Message:          &waE2E.Message{Conversation: proto.String(text)},
			MessageTimestamp: proto.Uint64(tsSec + id)}}
	}
	dm := &waHistorySync.Conversation{ID: proto.String("101@lid"), UnreadCount: proto.Uint32(3),
		Pinned: proto.Uint32(uint32(tsSec)), MuteEndTime: proto.Uint64(tsSec + 3600),
		EndOfHistoryTransfer: proto.Bool(true)}
	for i := range uint64(1200) {
		dm.Messages = append(dm.Messages, webMsg("101@lid", "", i, fmt.Sprintf("eski mesaj %d", i)))
	}
	group := &waHistorySync.Conversation{ID: proto.String("120363001@g.us"), Name: proto.String("Aile"), Archived: proto.Bool(true),
		Participant: []*waHistorySync.GroupParticipant{
			{UserJID: proto.String("101@lid"), Rank: waHistorySync.GroupParticipant_SUPERADMIN.Enum()},
			{UserJID: proto.String("202@lid")},
		},
		Messages: []*waHistorySync.HistorySyncMsg{webMsg("120363001@g.us", "202@lid", 5000, "grupta eski")}}
	return &waHistorySync.HistorySync{
		SyncType:      waHistorySync.HistorySync_INITIAL_BOOTSTRAP.Enum(),
		Conversations: []*waHistorySync.Conversation{dm, group, {ID: proto.String("status@broadcast")}},
		Pushnames:     []*waHistorySync.Pushname{{ID: proto.String("101@lid"), Pushname: proto.String("Ali")}, {ID: proto.String("202@lid"), Pushname: proto.String("-")}},
		PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{
			{PnJID: proto.String("905551112233@s.whatsapp.net"), LidJID: proto.String("101@lid")},
		},
	}
}

// inline wraps a sync in a notification with the payload inline, as the
// initial bootstrap arrives: no download is needed.
func inline(t *testing.T, h *waHistorySync.HistorySync) *waE2E.HistorySyncNotification {
	t.Helper()
	raw, err := proto.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	z := zlib.NewWriter(&b)
	_, _ = z.Write(raw)
	_ = z.Close()
	return &waE2E.HistorySyncNotification{InitialHistBootstrapInlinePayload: b.Bytes(),
		SyncType: waE2E.HistorySyncType_INITIAL_BOOTSTRAP.Enum()}
}

func notificationMsg(n *waE2E.HistorySyncNotification, fromMe bool) *events.Message {
	return &events.Message{Info: types.MessageInfo{
		MessageSource: types.MessageSource{Chat: aliJID, Sender: aliJID, IsFromMe: fromMe}, ID: "NOTIF1", Timestamp: ts,
	}, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_HISTORY_SYNC_NOTIFICATION.Enum(), HistorySyncNotification: n}}}
}

func TestHistoryEventsAreChunked(t *testing.T) {
	a := testAdapter(t)
	chunks := a.historyEvents(t.Context(), testSync())
	if len(chunks) != 5 { // links and names, the DM in 3 chunks, the group
		t.Fatalf("%d chunks", len(chunks))
	}
	first := chunks[0]
	if l := first[0].(domain.IdentityLinked); l.LID != "101@lid" || len(first) != 2 {
		t.Fatalf("first chunk = %+v; a push name of \"-\" is no name", first)
	}
	for _, c := range chunks[1:4] {
		n := 0
		for _, ev := range c {
			if m, ok := ev.(domain.MessageReceived); ok {
				n++
				if m.Source != domain.SourceHistory {
					t.Fatal("history messages are history")
				}
			}
		}
		if n > historyChunk {
			t.Fatalf("a chunk of %d messages", n)
		}
	}
	meta := chunks[1][0].(domain.ChatUpdated)
	if meta.UnreadCount != 3 || meta.PinnedAt.IsZero() || meta.MutedUntil.IsZero() || !meta.HistoryComplete ||
		!meta.Fields.Has(domain.ChatFieldHistoryComplete) || meta.Fields.Has(domain.ChatFieldTitle) {
		t.Fatalf("DM settings = %+v", meta)
	}
	g := chunks[4]
	if c := g[0].(domain.ChatUpdated); c.Title != "Aile" || !c.Archived {
		t.Fatalf("group settings = %+v", c)
	}
	if m := g[1].(domain.GroupMembersChanged); !m.Full || len(m.Members) != 2 || m.Members[0].Role != domain.RoleSuperAdmin {
		t.Fatalf("group members = %+v", m)
	}
	if m := g[2].(domain.MessageReceived); m.Message.SenderJID != "202@lid" || m.Message.ChatJID != "120363001@g.us" {
		t.Fatalf("group message = %+v", m.Message)
	}
}

// TestOnDemandHistory: an answer to a request for older messages stores
// the messages and no chat state; a conversation without messages means
// the phone has no older ones.
func TestOnDemandHistory(t *testing.T) {
	a := testAdapter(t)
	h := testSync()
	h.SyncType = waHistorySync.HistorySync_ON_DEMAND.Enum()
	h.Conversations = append(h.Conversations, &waHistorySync.Conversation{ID: proto.String("303@lid"), UnreadCount: proto.Uint32(9)})
	var evs []domain.Event
	for _, c := range a.historyEvents(t.Context(), h) {
		evs = append(evs, c...)
	}
	complete := map[domain.JID]bool{}
	messages := 0
	for _, ev := range evs {
		switch e := ev.(type) {
		case domain.ChatUpdated:
			if e.Fields != domain.ChatFieldHistoryComplete || !e.HistoryComplete {
				t.Fatalf("an on-demand answer must not change chat state: %+v", e)
			}
			complete[e.ChatJID] = true
		case domain.GroupMembersChanged:
			t.Fatalf("an on-demand answer must not replace members: %+v", e)
		case domain.MessageReceived:
			messages++
		}
	}
	if messages != 1201 || !complete["101@lid"] || !complete["303@lid"] || complete["120363001@g.us"] {
		t.Fatalf("%d messages, complete %v", messages, complete)
	}
}

func TestRequestHistoryNeedsAConnection(t *testing.T) {
	a := testAdapter(t)
	err := a.RequestHistory(t.Context(), "101@lid", backend.HistoryAnchor{WAID: "X", Timestamp: ts}, 50)
	if !errors.Is(err, backend.ErrNotConnected) {
		t.Fatalf("RequestHistory offline = %v", err)
	}
	if err := a.RequestHistory(t.Context(), "nope", backend.HistoryAnchor{}, 50); err == nil {
		t.Fatal("an invalid chat must be rejected")
	}
}

func TestMuteEnd(t *testing.T) {
	for v, want := range map[uint64]time.Time{
		0: {}, tsSec: ts, tsSec * 1000: ts, 1 << 62: domain.MuteForever,
	} {
		if got := muteEnd(v); !got.Equal(want) {
			t.Errorf("muteEnd(%d) = %v, want %v", v, got, want)
		}
	}
}

// flakySink fails its first deliveries, like a full disk, and records the
// rest.
type flakySink struct {
	mu     sync.Mutex
	fail   int
	events []domain.Event
	next   func(context.Context, []domain.Event) error
}

func (f *flakySink) Deliver(ctx context.Context, evs []domain.Event) error {
	f.mu.Lock()
	if f.fail > 0 {
		f.fail--
		f.mu.Unlock()
		return errors.New("disk full")
	}
	f.events = append(f.events, evs...)
	f.mu.Unlock()
	if f.next != nil {
		return f.next(ctx, evs)
	}
	return nil
}

func TestHistoryIsQueuedStoredAndDone(t *testing.T) {
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
	rdb, err := tstore.Open(t.Context(), path, tstore.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	r := tstore.NewReader(rdb)

	a := linked(t, New(openTestSession(t, filepath.Join(t.TempDir(), "session.db")), Options{HistoryRetry: 5 * time.Millisecond}))
	n := inline(t, testSync())
	if !a.handle(t.Context(), w, notificationMsg(n, false)) {
		t.Fatal("a forged notification is acknowledged and ignored")
	}
	if pending, _ := r.PendingHistory(t.Context()); len(pending) != 0 {
		t.Fatal("a notification that is not from this account must not be queued")
	}
	if !a.handle(t.Context(), w, notificationMsg(n, true)) {
		t.Fatal("the notification must be stored")
	}
	pending, err := r.PendingHistory(t.Context())
	if err != nil || len(pending) != 1 || pending[0].ID != "NOTIF1" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}

	// The first two deliveries fail, as on a full disk: the worker retries.
	sink := &flakySink{fail: 2, next: w.Deliver}
	job, _ := a.history.next()
	if !a.syncHistory(t.Context(), sink, job) {
		t.Fatal("syncHistory gave up")
	}
	if pending, _ := r.PendingHistory(t.Context()); len(pending) != 0 {
		t.Fatalf("a stored sync must leave the queue: %+v", pending)
	}
	dm, ok, err := r.ChatID(t.Context(), "905551112233@s.whatsapp.net")
	if err != nil || !ok {
		t.Fatalf("the DM is found by number after the mapping: %v %v", ok, err)
	}
	chats, _ := r.ChatsByID(t.Context(), []domain.ChatID{dm})
	if c := chats[0]; c.UnreadCount != 3 || !c.IsPinned() || !c.HistoryComplete {
		t.Fatalf("chat = %+v", c)
	}
	var msgs int
	if err := rdb.QueryRowContext(t.Context(), "SELECT count(*) FROM messages").Scan(&msgs); err != nil || msgs != 1201 {
		t.Fatalf("%d messages, %v", msgs, err)
	}
	ids, _ := r.Identities(t.Context())
	for i := range ids {
		if ids[i].LID == "101@lid" && ids[i].PushName != "Ali" {
			t.Fatalf("push name = %q", ids[i].PushName)
		}
	}

	// After a restart, a sync still queued is processed again; writing it
	// twice changes nothing.
	b := linked(t, New(openTestSession(t, filepath.Join(t.TempDir(), "session2.db")), Options{HistoryRetry: 5 * time.Millisecond}))
	b.Resume([]domain.HistoryQueued{{ID: "NOTIF1", Notification: mustMarshal(t, n)}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { b.runHistory(ctx, w); close(done) }()
	waitFor := time.Now().Add(5 * time.Second)
	for {
		var again int
		_ = rdb.QueryRowContext(t.Context(), "SELECT count(*) FROM messages").Scan(&again)
		if again == 1201 && b.history.len() == 0 {
			break
		}
		if time.Now().After(waitFor) {
			t.Fatalf("%d messages after the resumed sync", again)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

// linked turns a test adapter's device into a linked one, as after
// pairing: whatsmeow gives a device its stores only then.
func linked(t *testing.T, a *Adapter) *Adapter {
	t.Helper()
	a.sess.device.ID = &types.JID{User: "905550000000", Device: 1, Server: types.DefaultUserServer}
	a.sess.device.LID = types.JID{User: "100", Device: 1, Server: types.HiddenUserServer}
	sig := make([]byte, 64)
	a.sess.device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{0}, AccountSignature: sig,
		AccountSignatureKey: make([]byte, 32), DeviceSignature: sig}
	if err := a.sess.container.PutDevice(t.Context(), a.sess.device); err != nil {
		t.Fatal(err)
	}
	return a
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHistoryStopsWithItsContext(t *testing.T) {
	a := linked(t, New(openTestSession(t, filepath.Join(t.TempDir(), "session.db")), Options{HistoryRetry: time.Millisecond}))
	ctx, cancel := context.WithCancel(t.Context())
	sink := &flakySink{fail: 1 << 30}
	job := domain.HistoryQueued{ID: "X", Notification: mustMarshal(t, inline(t, testSync()))}
	done := make(chan bool, 1)
	go func() { done <- a.syncHistory(ctx, sink, job) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a sync cut short must stay queued")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("syncHistory ignored its context")
	}
	// An unreadable notification is dropped rather than retried forever.
	if !a.syncHistory(t.Context(), &flakySink{}, domain.HistoryQueued{ID: "bad", Notification: []byte{0xff, 0xff}}) {
		t.Fatal("an unreadable notification is done")
	}
}
