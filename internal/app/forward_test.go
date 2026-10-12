// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// flakyForward fails its first forwards.
type flakyForward struct {
	*fake.Backend
	fails atomic.Int32
}

func (f *flakyForward) Forward(ctx context.Context, req backend.ForwardRequest) (backend.SendResult, []byte, error) {
	if f.fails.Add(-1) >= 0 {
		return backend.SendResult{}, nil, errors.New("network down")
	}
	return f.Backend.Forward(ctx, req)
}

// TestForward: a forward is stored in the target chat as a pending copy
// marked forwarded, with the source's content, media and cached file, and
// sent with the source's protocol message; a failed one is forwarded
// again; what cannot be forwarded is refused before anything is stored.
func TestForward(t *testing.T) {
	fb := fake.New(fake.Config{Seed: 4, Chats: 2, GroupRatio: 0.5, MembersPerGroup: 2, Tick: time.Millisecond})
	flaky := &flakyForward{Backend: fb}
	flaky.fails.Store(1)
	me := fb.Me()
	p, err := openPipeline(t.Context(), filepath.Join(t.TempDir(), "tarcin.db"), pipelineOptions{MeLID: me.LID, MePN: me.PN, Backend: flaky,
		Media: &mediaOptions{Dir: t.TempDir(), Limit: 1 << 20, Cap: 1 << 30, Open: []string{"true"}, start: func([]string) error { return nil }}})
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
	g, peer := fb.Chats()[0], fb.Chats()[1]
	waitUntil(t, "the connection", func() bool { return fb.SetAvailable(t.Context(), false) == nil })
	in := func(waid string, kind domain.MessageKind) domain.IncomingMessage {
		return domain.IncomingMessage{ChatJID: g.JID, SenderJID: g.Members[0].LID, WAID: domain.WAMessageID(waid),
			Timestamp: time.Now(), Kind: kind, Body: "body of " + waid}
	}
	text, image, once := in("T1", domain.KindText), in("I1", domain.KindImage), in("V1", domain.KindImage)
	text.Flags = domain.FlagForwarded
	image.Raw, image.Media = []byte("image message"), &domain.Media{MIME: "image/jpeg", Size: 32, Width: 4, Height: 3}
	once.Raw, once.Media, once.Flags = []byte("view once"), &domain.Media{MIME: "image/jpeg", Size: 32}, domain.FlagViewOnce
	var events []domain.Event
	for _, m := range []domain.IncomingMessage{text, image, once} {
		events = append(events, domain.MessageReceived{Source: domain.SourceLive, Message: m})
	}
	if err := p.Deliver(t.Context(), events); err != nil {
		t.Fatal(err)
	}
	group, _, _ := p.reader.ChatID(t.Context(), g.JID)
	var dm domain.ChatID
	waitUntil(t, "the direct chat", func() bool { id, ok, _ := p.reader.ChatID(t.Context(), peer.JID); dm = id; return ok })
	get := func(chat domain.ChatID, waid domain.WAMessageID) domain.Message {
		m, _, _ := p.reader.MessageByWAID(t.Context(), chat, waid)
		return m
	}
	p.openMedia()(get(group, "I1"))() // downloads it: the copy shares the file
	s := newSender(flaky, p)
	defer s.close()
	forward := p.forward(s, flaky)
	copies := func() []domain.Message {
		page, _ := p.reader.Latest(t.Context(), dm, 10)
		var out []domain.Message
		for _, m := range page {
			if m.FromMe {
				out = append(out, m)
			}
		}
		return out
	}

	forward(get(group, "T1"), dm) // fails once
	waitUntil(t, "the failed forward", func() bool { c := copies(); return len(c) == 1 && c[0].Status == domain.StatusFailed })
	failed := copies()[0]
	if failed.Body != "body of T1" || !failed.Flags.Has(domain.FlagForwarded) || failed.Kind != domain.KindText {
		t.Fatalf("copy = %+v", failed)
	}
	p.retry(s)(failed)
	waitUntil(t, "the retry", func() bool { return len(fb.Forwarded()) == 1 })
	if f := fb.Forwarded()[0]; f.WAID != failed.WAID || f.Body != "body of T1" || f.Forwarded {
		t.Fatalf("forwarded %+v; a retry sends the same copy and does not count again", f)
	}

	forward(get(group, "I1"), dm)
	waitUntil(t, "the media forward", func() bool { return len(fb.Forwarded()) == 2 })
	f := fb.Forwarded()[1]
	if f.Kind != domain.KindImage || string(f.Raw) != "image message" || f.Body != "body of I1" || f.Forwarded {
		t.Fatalf("forwarded %+v", f)
	}
	waitUntil(t, "the stored copy", func() bool { c := copies(); return len(c) == 2 && c[1].Status == domain.StatusSent })
	img := copies()[1]
	if img.Kind != domain.KindImage || img.Media == nil || img.Media.Width != 4 || img.Media.State != domain.MediaLocal || !img.Flags.Has(domain.FlagForwarded) {
		t.Fatalf("copy = %+v (media %+v)", img, img.Media)
	}
	src, _, _ := p.reader.MediaSource(t.Context(), dm, img.WAID)
	orig, _, _ := p.reader.MediaSource(t.Context(), group, "I1")
	if src.LocalName == "" || src.LocalName != orig.LocalName || string(src.Raw) != "image message" {
		t.Fatalf("copy's media %+v, original's %+v", src, orig)
	}

	forward(get(group, "T1"), dm)
	waitUntil(t, "the second text forward", func() bool { return len(fb.Forwarded()) == 3 })
	if f := fb.Forwarded()[2]; !f.Forwarded || f.WAID == failed.WAID {
		t.Fatalf("forwarded %+v; a forwarded text says so when forwarded again", f)
	}

	forward(get(group, "V1"), dm)
	forward(domain.Message{ChatID: group, WAID: "GONE", Kind: domain.KindText}, dm)
	waitUntil(t, "the refusals", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(slices.DeleteFunc(slices.Clone(r.notices), func(n string) bool { return !strings.HasPrefix(n, "Message not forwarded") })) == 2
	})
	if n := len(copies()); n != 3 || len(fb.Forwarded()) != 3 {
		t.Fatalf("%d copies, %d forwards; refused forwards store nothing", n, len(fb.Forwarded()))
	}
}
