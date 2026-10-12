// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
)

func TestExtensionAndSaveName(t *testing.T) {
	for in, want := range map[string]string{
		"image/jpeg": ".jpg", "audio/ogg; codecs=opus": ".ogg", "application/x-sh": ".bin", "": ".bin", "nonsense": ".bin",
	} {
		if got := extension(in); got != want {
			t.Errorf("extension(%q) = %q, want %q", in, got, want)
		}
	}
	at := time.Date(2026, 10, 11, 14, 2, 3, 0, time.Local)
	for _, tt := range []struct {
		file, mime, want string
	}{
		{"rapor.pdf", "application/pdf", "rapor.pdf"},
		{"../../.bashrc", "text/plain", "bashrc.txt"},
		{"/etc/passwd", "", "passwd.bin"},
		{"evil\u202eexe.pdf", "application/pdf", "evilexe.pdf"},
		{"...", "image/png", "media.png"},
		{"", "image/jpeg", "tarcin image 2026-10-11 14-02-03.jpg"},
		{`C:\\Users\\x\\a.txt`, "text/plain", "a.txt"},
		{strings.Repeat("ş", 300) + ".pdf", "application/pdf", ""},
	} {
		got := saveName(&domain.Message{Kind: domain.KindImage, Timestamp: at}, &domain.Media{FileName: tt.file, MIME: tt.mime})
		switch {
		case tt.want == "":
			if len(got) > 200 || !strings.HasSuffix(got, ".pdf") {
				t.Errorf("a long name must be cut to 200 bytes with its extension: %d bytes %q", len(got), got[len(got)-10:])
			}
		case got != tt.want:
			t.Errorf("saveName(%q) = %q, want %q", tt.file, got, tt.want)
		}
	}
}

func TestSaveCopyNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.pdf"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	for range 2 {
		dst, err := saveCopy(src, dir, "a.pdf")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, filepath.Base(dst))
	}
	if !slices.Equal(got, []string{"a (1).pdf", "a (2).pdf"}) {
		t.Fatalf("saved as %v", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.pdf")); string(b) != "mine" { //nolint:gosec // the test's file
		t.Fatal("an existing file was replaced")
	}
	info, _ := os.Stat(filepath.Join(dir, "a (1).pdf"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
}

func TestOpenerChoice(t *testing.T) {
	h := &mediaHandler{o: mediaOptions{Open: []string{"xdg-open"}, Openers: map[string][]string{
		"image/*": {"imv"}, "application/pdf": {"zathura", "--fork"},
	}}}
	for mime, want := range map[string]string{"image/png": "imv", "application/pdf": "zathura", "video/mp4": "xdg-open", "bad": "xdg-open"} {
		if got := h.opener(mime)[0]; got != want {
			t.Errorf("opener(%q) = %q, want %q", mime, got, want)
		}
	}
}

// TestMediaDownloadCacheOpenSave: media is downloaded once into the
// content-addressed cache, opened, saved, refused beyond the limit, and
// evicted least recently used first.
func TestMediaDownloadCacheOpenSave(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 1, GroupRatio: 1, MembersPerGroup: 2, Tick: time.Millisecond})
	me := fb.Me()
	cache, save := t.TempDir(), t.TempDir()
	var mu sync.Mutex
	var opened [][]string
	opts := &mediaOptions{Dir: cache, Limit: 1 << 20, Cap: 40, SaveDir: save, Open: []string{"xdg-open"},
		start: func(argv []string) error { mu.Lock(); defer mu.Unlock(); opened = append(opened, argv); return nil }}
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{MeLID: me.LID, MePN: me.PN, Backend: fb, Media: opts})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	g := fb.Chats()[0]
	media := func(waid string, size int64) domain.Event {
		return domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{ChatJID: g.JID, SenderJID: g.Members[0].LID,
			WAID: domain.WAMessageID(waid), Timestamp: time.Now(), Kind: domain.KindImage, Raw: []byte{1},
			Media: &domain.Media{MIME: "image/jpeg", Size: size, FileName: "kedi.jpg"}}}
	}
	waitUntil(t, "the connection", func() bool { return fb.SetAvailable(t.Context(), false) == nil })
	if err := p.Deliver(t.Context(), []domain.Event{media("A", 32), media("B", 32), media("HUGE", 2<<20)}); err != nil {
		t.Fatal(err)
	}
	chat, _, _ := p.reader.ChatID(t.Context(), g.JID)
	get := func(waid string) domain.Message {
		m, _, _ := p.reader.MessageByWAID(t.Context(), chat, domain.WAMessageID(waid))
		return m
	}
	notice := func(m domain.Message, save bool) string {
		fn := p.openMedia()
		if save {
			fn = p.saveMedia()
		}
		b, _ := fn(m)().(ui.BatchMsg)
		return b.Notice
	}

	if n := notice(get("A"), false); !strings.Contains(n, "Opened with xdg-open") {
		t.Fatalf("open: %q", n)
	}
	a := get("A")
	if a.Media.State != domain.MediaLocal || fb.Downloads() != 1 || len(opened) != 1 {
		t.Fatalf("state %v, downloads %d, opened %v", a.Media.State, fb.Downloads(), opened)
	}
	path := opened[0][1]
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || filepath.Dir(path) != cache || !strings.HasSuffix(path, ".jpg") || len(filepath.Base(path)) != 64+4 {
		t.Fatalf("cache file %q: %v %v", path, info, err)
	}
	if n := notice(get("A"), true); !strings.Contains(n, "Saved to") {
		t.Fatalf("save: %q", n)
	}
	if fb.Downloads() != 1 {
		t.Fatal("cached media is not downloaded again")
	}
	if b, err := os.ReadFile(filepath.Join(save, "kedi.jpg")); err != nil || len(b) == 0 { //nolint:gosec // the test's file
		t.Fatalf("saved file: %v", err)
	}
	if n := notice(get("HUGE"), false); !strings.Contains(n, "larger than the 1 MiB limit") {
		t.Fatalf("too large: %q", n)
	}
	// Same content: one cache file. The cap (40 bytes) holds one file.
	if n := notice(get("B"), false); !strings.Contains(n, "Opened") {
		t.Fatalf("open B: %q", n)
	}
	files, _ := os.ReadDir(cache)
	if len(files) != 1 {
		t.Fatalf("cache holds %d files", len(files))
	}
	if n := notice(domain.Message{ChatID: chat, WAID: "NONE", Kind: domain.KindImage}, false); !strings.Contains(n, "no media") {
		t.Fatalf("no media: %q", n)
	}
}

func TestEvictLeastRecentlyUsed(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 1, GroupRatio: 1, MembersPerGroup: 2, Tick: time.Millisecond})
	p, _ := newTestPipeline(t, fb.Me())
	cache := t.TempDir()
	h := newMediaHandler(p, mediaOptions{Dir: cache, Limit: 1 << 20, Cap: 25})
	defer h.close()
	g := fb.Chats()[0]
	old := time.Now().Add(-time.Hour)
	for i, name := range []string{"old.jpg", "mid.jpg", "new.jpg"} {
		path := filepath.Join(cache, name)
		if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := old.Add(time.Duration(i) * time.Minute)
		_ = os.Chtimes(path, at, at)
		waid := domain.WAMessageID(strings.TrimSuffix(name, ".jpg"))
		if err := p.Deliver(t.Context(), []domain.Event{
			domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{ChatJID: g.JID, SenderJID: g.Members[0].LID,
				WAID: waid, Timestamp: time.Now(), Kind: domain.KindImage, Media: &domain.Media{MIME: "image/jpeg"}}},
			domain.MediaChanged{ChatJID: g.JID, TargetWAID: waid, State: domain.MediaLocal, LocalName: name},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(cache, ".download-1"), make([]byte, 100), 0o600) // in progress: not counted
	h.evict(t.Context(), "old.jpg")
	var left []string
	files, _ := os.ReadDir(cache)
	for _, f := range files {
		left = append(left, f.Name())
	}
	if !slices.Equal(left, []string{".download-1", "new.jpg", "old.jpg"}) {
		t.Fatalf("left %v: the least recently used goes first, the kept one stays", left)
	}
	chat, _, _ := p.reader.ChatID(t.Context(), g.JID)
	if src, _, _ := p.reader.MediaSource(t.Context(), chat, "mid"); src.Media.State != domain.MediaRemote || src.LocalName != "" {
		t.Fatalf("the evicted media is remote again: %+v", src)
	}
}

func TestExamineAttachments(t *testing.T) {
	dir := t.TempDir()
	h := &mediaHandler{o: mediaOptions{UploadLimit: 1 << 20}}
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	photo := write("photo.dat", buf.Bytes()) // the content decides
	a, err := h.examine(photo)
	if err != nil || a.kind != domain.KindImage || a.mime != "image/jpeg" || a.width != 40 || a.height != 30 {
		t.Fatalf("photo = %+v, %v", a, err)
	}
	pdf := write("rapor.pdf", []byte("%PDF-1.4 hello"))
	if a, err := h.examine(pdf); err != nil || a.kind != domain.KindDocument || a.mime != "application/pdf" || a.name != "rapor.pdf" {
		t.Fatalf("pdf = %+v, %v", a, err)
	}
	if _, err := h.examine(write("empty.txt", nil)); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty: %v", err)
	}
	if _, err := h.examine(dir); err == nil {
		t.Fatal("a directory is not sent")
	}
	if _, err := h.examine(write("big.bin", make([]byte, 2<<20))); err == nil || !strings.Contains(err.Error(), "1 MiB limit") {
		t.Fatalf("too large: %v", err)
	}
	if _, err := h.examine(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing file is an error")
	}
}

// flakyMedia fails its first sends of media.
type flakyMedia struct {
	*fake.Backend
	fails atomic.Int32
}

func (f *flakyMedia) SendMedia(ctx context.Context, req backend.MediaRequest) (backend.SendResult, []byte, error) {
	if f.fails.Add(-1) >= 0 {
		return backend.SendResult{}, nil, errors.New("network down")
	}
	return f.Backend.SendMedia(ctx, req)
}

// TestSendAttachment: an attachment is stored as a pending media message,
// kept in the cache, sent with its caption, and its protocol message is
// stored; a failed one is sent again from the cache, also once the
// original file is gone.
func TestSendAttachment(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 1, GroupRatio: 1, MembersPerGroup: 2, Tick: time.Millisecond})
	flaky := &flakyMedia{Backend: fb}
	flaky.fails.Store(1)
	me := fb.Me()
	cache := t.TempDir()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{MeLID: me.LID, MePN: me.PN, Backend: flaky,
		Media: &mediaOptions{Dir: cache, Limit: 1 << 20, UploadLimit: 1 << 20, Cap: 1 << 30, Open: []string{"true"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := newRecordingUI()
	if err := p.start(r); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = fb.Run(ctx, p) }()
	g := fb.Chats()[0]
	var chat domain.ChatID
	waitUntil(t, "the group", func() bool { id, ok, _ := p.reader.ChatID(t.Context(), g.JID); chat = id; return ok })
	file := filepath.Join(t.TempDir(), "rapor.pdf")
	if err := os.WriteFile(file, []byte("%PDF-1.4 rapor"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSender(flaky, p)
	defer s.close()
	res, ok := p.attach(s, flaky)(ui.Outgoing{Chat: chat, Text: "rapor ekte"}, file)().(ui.AttachResultMsg)
	if !ok || res.Err != nil {
		t.Fatalf("attach = %+v", res)
	}
	last := func() domain.Message {
		page, _ := p.reader.Latest(t.Context(), chat, 1)
		if len(page) == 0 {
			return domain.Message{}
		}
		return page[0]
	}
	waitUntil(t, "the failed send", func() bool { m := last(); return m.Status == domain.StatusFailed })
	failed := last()
	if failed.Kind != domain.KindDocument || failed.Body != "rapor ekte" || failed.Media == nil || failed.Media.State != domain.MediaLocal {
		t.Fatalf("pending = %+v", failed)
	}
	_ = os.Remove(file) // the cache has a copy
	p.retry(s)(failed)
	waitUntil(t, "the retry", func() bool { return len(fb.SentMedia()) == 1 })
	sent := fb.SentMedia()[0]
	if sent.Kind != domain.KindDocument || sent.MIME != "application/pdf" || sent.FileName != "rapor.pdf" || sent.Caption != "rapor ekte" || sent.WAID != failed.WAID {
		t.Fatalf("sent %+v", sent)
	}
	waitUntil(t, "the stored message", func() bool { m := last(); return m.Status == domain.StatusSent })
	src, _, _ := p.reader.MediaSource(t.Context(), chat, failed.WAID)
	if len(src.Raw) == 0 || src.Media.FileName != "rapor.pdf" {
		t.Fatalf("source = %+v", src)
	}
	if info, err := os.Stat(filepath.Join(cache, src.LocalName)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the attachment must be in the cache, private: %v, %v", info, err)
	}
	noTemporaryFiles(t, cache)
}

// noTemporaryFiles fails if the cache holds a download, upload or copy
// in progress.
func noTemporaryFiles(t *testing.T, cache string) {
	t.Helper()
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if isMediaTemp(e.Name()) {
			t.Errorf("a temporary file is left in the cache: %s", e.Name())
		}
	}
}

// brokenDownload writes part of the media and then fails.
type brokenDownload struct{ *fake.Backend }

func (b *brokenDownload) DownloadMedia(_ context.Context, _ []byte, f *os.File, _ int64) error {
	if _, err := f.WriteString("half a fi"); err != nil {
		return err
	}
	return errors.New("connection reset")
}

// TestFailedDownloadLeavesNothing: a download that fails half way leaves
// no temporary file, and the media is marked failed.
func TestFailedDownloadLeavesNothing(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 1, GroupRatio: 1, MembersPerGroup: 2, Tick: time.Millisecond})
	broken := &brokenDownload{Backend: fb}
	me := fb.Me()
	cache := t.TempDir()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{MeLID: me.LID, MePN: me.PN, Backend: broken,
		Media: &mediaOptions{Dir: cache, Limit: 1 << 20, Cap: 1 << 30, Open: []string{"true"}, start: func([]string) error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()
	g := fb.Chats()[0]
	if err := p.Deliver(t.Context(), []domain.Event{domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
		ChatJID: g.JID, SenderJID: g.Members[0].LID, WAID: "M", Timestamp: time.Now(), Kind: domain.KindImage, Raw: []byte{1},
		Media: &domain.Media{MIME: "image/jpeg", Size: 32},
	}}}); err != nil {
		t.Fatal(err)
	}
	chat, _, _ := p.reader.ChatID(t.Context(), g.JID)
	msg, _, _ := p.reader.MessageByWAID(t.Context(), chat, "M")
	if b, _ := p.openMedia()(msg)().(ui.BatchMsg); !strings.Contains(b.Notice, "connection reset") {
		t.Fatalf("notice %q", b.Notice)
	}
	noTemporaryFiles(t, cache)
	if m, _, _ := p.reader.MessageByWAID(t.Context(), chat, "M"); m.Media == nil || m.Media.State != domain.MediaFailed {
		t.Fatalf("media = %+v", m.Media)
	}
}

// TestExpireDeletesMessagesAndFiles: a disappearing message whose time is
// up goes, with its cached media file.
func TestExpireDeletesMessagesAndFiles(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 1, GroupRatio: 1, MembersPerGroup: 2, Tick: time.Millisecond})
	me := fb.Me()
	cache := t.TempDir()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{MeLID: me.LID, MePN: me.PN, Backend: fb,
		Media: &mediaOptions{Dir: cache, Limit: 1 << 20, UploadLimit: 1 << 20, Cap: 1 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()
	g := fb.Chats()[0]
	if err := os.WriteFile(filepath.Join(cache, "gone.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := p.Deliver(t.Context(), []domain.Event{
		domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{ChatJID: g.JID, SenderJID: g.Members[0].LID,
			WAID: "GONE", Timestamp: old, Kind: domain.KindImage, Media: &domain.Media{MIME: "image/jpeg"}, Expiration: time.Hour}},
		domain.MediaChanged{ChatJID: g.JID, TargetWAID: "GONE", State: domain.MediaLocal, LocalName: "gone.jpg"},
		domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{ChatJID: g.JID, SenderJID: g.Members[0].LID,
			WAID: "STAYS", Timestamp: old, Kind: domain.KindText, Body: "kalır", Expiration: 24 * time.Hour}},
	}); err != nil {
		t.Fatal(err)
	}
	p.expire()
	chat, _, _ := p.reader.ChatID(t.Context(), g.JID)
	if _, ok, _ := p.reader.MessageByWAID(t.Context(), chat, "GONE"); ok {
		t.Fatal("the expired message must be gone")
	}
	if _, ok, _ := p.reader.MessageByWAID(t.Context(), chat, "STAYS"); !ok {
		t.Fatal("the other stays until its time")
	}
	waitUntil(t, "its file to be deleted", func() bool {
		_, err := os.Stat(filepath.Join(cache, "gone.jpg"))
		return os.IsNotExist(err)
	})
}

// TestDeleteLocalAndRemoteDeletes: deleting a chat here removes its rows
// and the cache files no other message uses; a deletion from another
// device does the same.
func TestDeleteLocalAndRemoteDeletes(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 3, GroupRatio: 0.01, Tick: time.Millisecond})
	me := fb.Me()
	cache := t.TempDir()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{MeLID: me.LID, MePN: me.PN, Backend: fb,
		Media: &mediaOptions{Dir: cache, Limit: 1 << 20, UploadLimit: 1 << 20, Cap: 1 << 30}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.close(context.Background()) }()
	chats := fb.Chats()
	img := func(chat fake.Chat, waid, file string) []domain.Event {
		return []domain.Event{
			domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{ChatJID: chat.JID, SenderJID: chat.Peer.LID,
				WAID: domain.WAMessageID(waid), Timestamp: time.Now().Add(-time.Minute), Kind: domain.KindImage, Media: &domain.Media{MIME: "image/jpeg"}}},
			domain.MediaChanged{ChatJID: chat.JID, TargetWAID: domain.WAMessageID(waid), State: domain.MediaLocal, LocalName: file},
		}
	}
	for _, f := range []string{"only.jpg", "shared.jpg", "remote.jpg"} {
		if err := os.WriteFile(filepath.Join(cache, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	events := slices.Concat(img(chats[0], "A", "only.jpg"), img(chats[0], "B", "shared.jpg"), img(chats[1], "C", "shared.jpg"),
		img(chats[2], "D", "remote.jpg"))
	if err := p.Deliver(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	id0, _, _ := p.reader.ChatID(t.Context(), chats[0].JID)
	if b, _ := p.deleteLocal()(id0)().(ui.BatchMsg); !strings.Contains(b.Notice, "your phone keeps the chat") {
		t.Fatalf("notice %q", b.Notice)
	}
	if _, ok, _ := p.reader.ChatID(t.Context(), chats[0].JID); ok {
		t.Fatal("the chat must be gone")
	}
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(cache, name)); return err == nil }
	waitUntil(t, "only.jpg to be deleted", func() bool { return !exists("only.jpg") })
	if !exists("shared.jpg") {
		t.Fatal("a file another message uses stays")
	}
	// From another device.
	if err := p.Deliver(t.Context(), []domain.Event{domain.ChatDeleted{ChatJID: chats[2].JID, Before: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "remote.jpg to be deleted: a deletion from another device removes the files too", func() bool { return !exists("remote.jpg") })
	if !exists("shared.jpg") {
		t.Fatal("a file another message uses stays")
	}
}

// TestSweepCacheAtStart: a cache file no stored message uses, such as one
// a commit left unused just before tarcin stopped, goes at the next start;
// files in use and temporary ones are not the sweep's business.
func TestSweepCacheAtStart(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 1, GroupRatio: 1, MembersPerGroup: 2, Tick: time.Millisecond})
	me := fb.Me()
	cache, db := t.TempDir(), filepath.Join(t.TempDir(), "tarcin.db")
	open := func() *pipeline {
		p, err := openPipeline(t.Context(), db, pipelineOptions{MeLID: me.LID, MePN: me.PN, Backend: fb,
			Media: &mediaOptions{Dir: cache, Limit: 1 << 20, Cap: 1 << 30}})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.start(newRecordingUI()); err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := open()
	g := fb.Chats()[0]
	if err := p.Deliver(t.Context(), []domain.Event{
		domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{ChatJID: g.JID, SenderJID: g.Members[0].LID,
			WAID: "KEPT", Timestamp: time.Now(), Kind: domain.KindImage, Media: &domain.Media{MIME: "image/jpeg"}}},
		domain.MediaChanged{ChatJID: g.JID, TargetWAID: "KEPT", State: domain.MediaLocal, LocalName: "kept.jpg"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kept.jpg", "orphan.jpg", downloadTemp + "1"} {
		if err := os.WriteFile(filepath.Join(cache, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p = open()
	defer func() { _ = p.close(context.Background()) }()
	var left []string
	entries, _ := os.ReadDir(cache)
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if want := []string{downloadTemp + "1", "kept.jpg"}; !slices.Equal(left, want) {
		t.Fatalf("cache holds %v, want %v", left, want)
	}
}

func TestCachePathStaysInTheCache(t *testing.T) {
	o := &mediaOptions{Dir: "/cache/media"}
	if p, ok := o.cachePath("0a1b.jpg"); !ok || p != "/cache/media/0a1b.jpg" {
		t.Fatalf("cachePath = %q, %v", p, ok)
	}
	for _, name := range []string{"", ".", "..", "../x", "a/b", "/etc/passwd", downloadTemp + "1"} {
		if p, ok := o.cachePath(name); ok {
			t.Errorf("cachePath(%q) = %q; a stored name must not lead out of the cache", name, p)
		}
	}
}
