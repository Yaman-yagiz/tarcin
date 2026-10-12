// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/logging"
	tstore "github.com/Yaman-yagiz/tarcin/internal/store"
)

func TestJIDConversion(t *testing.T) {
	for _, j := range []domain.JID{"123@lid", "905551112233@s.whatsapp.net", "120363001234567890@g.us", "status@broadcast", "123@newsletter"} {
		w, err := toJID(j)
		if err != nil {
			t.Fatalf("toJID(%s): %v", j, err)
		}
		if got := fromJID(w); got != j {
			t.Errorf("round trip %s → %s", j, got)
		}
	}
	device := types.JID{User: "905551112233", Device: 12, Server: types.DefaultUserServer}
	if got := fromJID(device); got != "905551112233@s.whatsapp.net" {
		t.Errorf("a device JID must map to its user: %s", got)
	}
	if fromJID(types.EmptyJID) != "" {
		t.Error("an empty JID converts to an empty one")
	}
	for _, bad := range []domain.JID{"not a jid@@", "905551112233:12@s.whatsapp.net", "x@unknown.server", ""} {
		if _, err := toJID(bad); !errors.Is(err, domain.ErrInvalidJID) {
			t.Errorf("toJID(%q): err = %v", bad, err)
		}
	}
}

func openTestSession(t testing.TB, path string) *Session {
	t.Helper()
	db, err := tstore.Open(t.Context(), path, tstore.RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenSession(t.Context(), db, nil)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return s
}

func TestOpenSessionWithTheStoreDriver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	s := openTestSession(t, path)
	if s.Paired() {
		t.Fatal("a fresh store holds an unpaired device")
	}
	if pn, lid := s.Account(); pn != "" || lid != "" {
		t.Fatalf("Account() = %q, %q before pairing", pn, lid)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening runs no migration twice.
	s = openTestSession(t, path)
	defer s.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session.db mode %v: it holds the device keys", info.Mode().Perm())
	}
}

func TestSetDeviceName(t *testing.T) {
	name, version := *store.DeviceProps.Os, store.DeviceProps.Version
	platform := store.DeviceProps.PlatformType
	defer func() {
		store.DeviceProps.Os, store.DeviceProps.Version, store.DeviceProps.PlatformType = &name, version, platform
	}()
	SetDeviceName(DefaultDeviceName, [3]uint32{0, 1, 0})
	if *store.DeviceProps.Os != "tarcin" || store.DeviceProps.GetVersion().GetSecondary() != 1 ||
		store.DeviceProps.GetPlatformType() != waCompanionReg.DeviceProps_DESKTOP {
		t.Fatalf("DeviceProps = %v", store.DeviceProps)
	}
}

// recordingSink records deliveries and fails them on request.
type recordingSink struct {
	mu     sync.Mutex
	events []domain.Event
	fail   bool
}

func (r *recordingSink) Deliver(_ context.Context, evs []domain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("not committed")
	}
	r.events = append(r.events, evs...)
	return nil
}

func TestOfflineCalls(t *testing.T) {
	s := openTestSession(t, filepath.Join(t.TempDir(), "session.db"))
	defer s.Close()
	a := New(s, Options{})
	if first, second := a.NewMessageID(), a.NewMessageID(); first == "" || first == second {
		t.Fatal("message IDs must be fresh")
	}
	if _, err := a.SendText(t.Context(), backend.SendRequest{ChatJID: "1@lid", Body: "x"}); !errors.Is(err, backend.ErrNotConnected) {
		t.Fatalf("SendText offline = %v", err)
	}
	if _, err := a.SendText(t.Context(), backend.SendRequest{ChatJID: "@@", Body: "x"}); !errors.Is(err, domain.ErrInvalidJID) {
		t.Fatalf("SendText to a bad JID = %v", err)
	}
	if err := a.MarkRead(t.Context(), "@@", "", nil, time.Now()); !errors.Is(err, domain.ErrInvalidJID) {
		t.Fatalf("MarkRead to a bad JID = %v", err)
	}
}

func TestConnectionEvents(t *testing.T) {
	s := openTestSession(t, filepath.Join(t.TempDir(), "session.db"))
	defer s.Close()
	a := New(s, Options{DeliverTimeout: time.Second})
	sink := &recordingSink{}
	for _, evt := range []any{
		&events.Connected{}, &events.Disconnected{}, &events.LoggedOut{}, &events.StreamReplaced{},
		&events.ClientOutdated{}, &events.TemporaryBan{Code: events.TempBanSentToTooManyPeople, Expire: time.Hour},
	} {
		if !a.handle(t.Context(), sink, evt) {
			t.Fatalf("%T was not acknowledged", evt)
		}
	}
	want := []domain.ConnectionState{domain.ConnOnline, domain.ConnDisconnected, domain.ConnLoggedOut,
		domain.ConnStreamReplace, domain.ConnOutdated, domain.ConnBanned}
	if len(sink.events) != len(want) {
		t.Fatalf("events = %+v", sink.events)
	}
	for i, ev := range sink.events {
		c := ev.(domain.ConnectionChanged)
		if c.State != want[i] {
			t.Errorf("event %d: %v, want %v", i, c.State, want[i])
		}
		if c.State.Terminal() && c.Reason == "" {
			t.Errorf("%v needs an explanation for the user", c.State)
		}
	}
	if ban := sink.events[5].(domain.ConnectionChanged); time.Until(ban.Until) < 59*time.Minute {
		t.Errorf("ban end = %v", ban.Until)
	}
	if !a.handle(t.Context(), sink, &events.Presence{}) {
		t.Error("events tarcin does not handle yet are acknowledged")
	}
	sink.fail = true
	if a.handle(t.Context(), sink, &events.Connected{}) {
		t.Error("an event that was not committed must not be acknowledged")
	}
}

// blockingSink never commits in time.
type blockingSink struct{}

func (blockingSink) Deliver(ctx context.Context, _ []domain.Event) error {
	<-ctx.Done()
	return ctx.Err()
}

// panickingSink panics, as a bug in normalisation would.
type panickingSink struct{}

func (panickingSink) Deliver(context.Context, []domain.Event) error { panic("canary 7c1e") }

func TestHandlerWaitsForTheCommitWithATimeout(t *testing.T) {
	s := openTestSession(t, filepath.Join(t.TempDir(), "session.db"))
	defer s.Close()
	var logged bytes.Buffer
	a := New(s, Options{DeliverTimeout: 50 * time.Millisecond, Log: slog.New(logging.NewHandler(&logged, slog.LevelInfo))})
	start := time.Now()
	if a.handle(t.Context(), blockingSink{}, &events.Connected{}) {
		t.Fatal("an event not committed in time must not be acknowledged")
	}
	if d := time.Since(start); d < 50*time.Millisecond || d > 2*time.Second {
		t.Fatalf("the handler waited %v, want the deliver timeout", d)
	}
	if a.handle(t.Context(), panickingSink{}, &events.Connected{}) {
		t.Fatal("a panic must withhold the acknowledgement")
	}
	for _, want := range []string{"event not stored", "context deadline exceeded", "event handler panicked", "panic=string"} {
		if !strings.Contains(logged.String(), want) {
			t.Fatalf("the log lacks %q:\n%s", want, logged.String())
		}
	}
	if strings.Contains(logged.String(), "7c1e") {
		t.Fatal("a panic's value may hold message content and must not be logged")
	}
	if !a.cli.SynchronousAck || !a.cli.EnableDecryptedEventBuffer || !a.cli.AutomaticMessageRerequestFromPhone ||
		!a.cli.UseRetryMessageStore || !a.cli.EnableAutoReconnect || a.cli.AutoReconnectHook == nil {
		t.Fatal("the client options of ADR-0003 must be set")
	}
}

func TestReconnectBackoffIsCapped(t *testing.T) {
	s := openTestSession(t, filepath.Join(t.TempDir(), "session.db"))
	defer s.Close()
	a := New(s, Options{})
	a.cli.AutoReconnectErrors = 100 // whatsmeow would wait 200 s next
	if !a.cli.AutoReconnectHook(errors.New("offline")) {
		t.Fatal("the hook keeps reconnecting")
	}
	if delay := time.Duration(a.cli.AutoReconnectErrors) * 2 * time.Second; delay > maxReconnectDelay {
		t.Fatalf("next delay %v", delay)
	}
	a.cli.AutoReconnectErrors = 3
	a.cli.AutoReconnectHook(nil)
	if a.cli.AutoReconnectErrors != 3 {
		t.Fatal("short backoffs are left alone")
	}
}

func TestKeepAliveAndStoppedSessions(t *testing.T) {
	s := openTestSession(t, filepath.Join(t.TempDir(), "session.db"))
	defer s.Close()
	a := New(s, Options{})
	sink := &recordingSink{}
	a.handle(t.Context(), sink, &events.KeepAliveTimeout{ErrorCount: 2})
	a.handle(t.Context(), sink, &events.KeepAliveRestored{})
	if c := sink.events[0].(domain.ConnectionChanged); c.State != domain.ConnConnecting || c.Reason == "" {
		t.Fatalf("keepalive timeout = %+v", c)
	}
	if c := sink.events[1].(domain.ConnectionChanged); c.State != domain.ConnOnline {
		t.Fatalf("keepalive restored = %+v", c)
	}
	a.handle(t.Context(), sink, &events.StreamReplaced{})
	if a.stopped.Load() != uint32(domain.ConnStreamReplace) {
		t.Fatal("a replaced session must stop automatic reconnects")
	}
	a.networkChanged() // must not reconnect; the device is not linked either way
	a.handle(t.Context(), sink, &events.Connected{})
	if a.stopped.Load() != 0 {
		t.Fatal("a new connection clears the stop")
	}
	a.Reconnect() // an unlinked device has nothing to reconnect
}

func TestWatchNetworkStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- watchNetwork(ctx, func() {}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watchNetwork: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchNetwork ignored its context")
	}
}

func TestWhatsmeowLogsAreRedacted(t *testing.T) {
	var buf bytes.Buffer
	l := newWALogger(slog.New(logging.NewHandler(&buf, slog.LevelDebug)), "whatsmeow/Client")
	sock := l.Sub("Socket")
	sock.Warnf("Error decrypting message %s from %s", "3EB0AA", types.NewJID("905551112233", types.DefaultUserServer))
	sock.Infof("Got %d/%s connect failure", 401, "logged out")
	l.Debugf("Received <message notify=%q>", "Ayşe")
	l.Errorf("broken: %v", errors.New("from 123456789012345@lid"))
	out := buf.String()
	for _, leak := range []string{"905551112233", "123456789012345", "Ayşe", "Received"} {
		if strings.Contains(out, leak) {
			t.Fatalf("the log has %q:\n%s", leak, out)
		}
	}
	for _, want := range []string{"level=WARN", "module=whatsmeow/Client/Socket", "3EB0AA from jid#", "401/logged out", "level=ERROR"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log lacks %q:\n%s", want, out)
		}
	}
	if newWALogger(nil, "x") == nil {
		t.Fatal("nil logs nothing, through whatsmeow's no-op logger")
	}
}

func TestLimitedFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	l := &limitedFile{File: f, limit: 10}
	if n, err := l.WriteString("012345"); n != 6 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := l.WriteString("6789X"); !errors.Is(err, errTooLarge) {
		t.Fatalf("growing past the limit: %v", err)
	}
	if _, err := l.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := l.WriteString("0123456789"); err != nil {
		t.Fatalf("a retry from the start fits: %v", err)
	}
	// io.Copy, as whatsmeow streams a download, uses ReadFrom.
	if _, err := l.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(l, strings.NewReader(strings.Repeat("x", 100))); !errors.Is(err, errTooLarge) {
		t.Fatalf("io.Copy past the limit: %v", err)
	}
	if info, _ := f.Stat(); info.Size() > 10 {
		t.Fatalf("the file grew to %d bytes", info.Size())
	}
	if _, err := l.WriteAt([]byte("ab"), 9); !errors.Is(err, errTooLarge) {
		t.Fatalf("WriteAt past the limit: %v", err)
	}
	if err := l.Truncate(11); !errors.Is(err, errTooLarge) {
		t.Fatalf("Truncate past the limit: %v", err)
	}
}

func TestDownloadMediaChecksFirst(t *testing.T) {
	a := testAdapter(t)
	f, _ := os.CreateTemp(t.TempDir(), "x")
	defer f.Close()
	if err := a.DownloadMedia(t.Context(), nil, f, 100); err == nil {
		t.Fatal("no stored details, no download")
	}
	big, _ := proto.Marshal(&waE2E.Message{ImageMessage: &waE2E.ImageMessage{FileLength: proto.Uint64(1000)}})
	if err := a.DownloadMedia(t.Context(), big, f, 100); !errors.Is(err, backend.ErrMediaTooLarge) {
		t.Fatalf("declared too large: %v", err)
	}
	small, _ := proto.Marshal(&waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileLength: proto.Uint64(10)}})
	if err := a.DownloadMedia(t.Context(), small, f, 100); !errors.Is(err, backend.ErrNotConnected) {
		t.Fatalf("offline: %v", err)
	}
	text, _ := proto.Marshal(&waE2E.Message{Conversation: proto.String("x")})
	if err := a.DownloadMedia(t.Context(), text, f, 100); err == nil {
		t.Fatal("a text message has nothing to download")
	}
}

func TestMediaMessage(t *testing.T) {
	up := &whatsmeow.UploadResponse{URL: "u", DirectPath: "/d", MediaKey: []byte{1}, FileEncSHA256: []byte{2}, FileSHA256: []byte{3}, FileLength: 9}
	img := mediaMessage(&backend.MediaRequest{Kind: domain.KindImage, MIME: "image/jpeg", Caption: "kedi", Width: 40, Height: 30}, up)
	if x := img.GetImageMessage(); x.GetCaption() != "kedi" || x.GetWidth() != 40 || x.GetDirectPath() != "/d" || x.GetFileLength() != 9 {
		t.Fatalf("image = %v", img)
	}
	doc := mediaMessage(&backend.MediaRequest{Kind: domain.KindDocument, MIME: "application/pdf", FileName: "a.pdf"}, up)
	if x := doc.GetDocumentMessage(); x.GetFileName() != "a.pdf" || x.Caption != nil || x.GetMimetype() != "application/pdf" {
		t.Fatalf("document = %v", doc)
	}
	audio := mediaMessage(&backend.MediaRequest{Kind: domain.KindAudio, MIME: "audio/ogg", Duration: 3 * time.Second}, up)
	if audio.GetAudioMessage().GetSeconds() != 3 {
		t.Fatalf("audio = %v", audio)
	}
	if c := content(img); c.kind != domain.KindImage || c.body != "kedi" {
		t.Fatalf("it normalises back: %+v", c)
	}
	a := testAdapter(t)
	if _, _, err := a.SendMedia(t.Context(), backend.MediaRequest{ChatJID: "101@lid", Kind: domain.KindImage}); !errors.Is(err, backend.ErrNotConnected) {
		t.Fatalf("offline: %v", err)
	}
}

func TestChatSettingsNeedAConnection(t *testing.T) {
	a := testAdapter(t)
	for _, err := range []error{
		a.MuteChat(t.Context(), "101@lid", domain.MuteForever),
		a.PinChat(t.Context(), "101@lid", true),
		a.ArchiveChat(t.Context(), "120363001234567890@g.us", true),
	} {
		if !errors.Is(err, backend.ErrNotConnected) {
			t.Fatalf("offline: %v", err)
		}
	}
	if err := a.PinChat(t.Context(), "bad", true); err == nil {
		t.Fatal("an invalid chat must be rejected")
	}
}
