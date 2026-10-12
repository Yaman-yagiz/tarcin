// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// maxSends is how many messages are sent at once across all chats
// (event-pipeline.md §3.4).
const maxSends = 4

// sendTimeout bounds one send.
const sendTimeout = 60 * time.Second

// sendJob is one message to send: a new one, or a failed one again.
type sendJob struct {
	chat   domain.ChatID
	waid   domain.WAMessageID
	body   string
	quoted domain.WAMessageID // the message this one answers, if any
	// mentions are the people body mentions as "@" and their user part.
	mentions []domain.JID
	// media is the attachment of a media message; body is its caption.
	// A retry of one finds the file in the cache.
	media *attachment
	// forward names the message this one copies, if it is a forward. A
	// retry of a forward names itself: it holds the copied content.
	forward *forwardSource
	kind    domain.MessageKind // of a retry
	at      time.Time
	retry   bool // already stored, as failed
}

// forwardSource names a stored message to forward.
type forwardSource struct {
	chat domain.ChatID
	waid domain.WAMessageID
}

// forwarded is what a forward copies from its source.
type forwarded struct {
	kind      domain.MessageKind
	body      string
	media     *domain.Media
	raw       []byte
	forwarded bool   // the source was itself forwarded
	local     string // the source's file in the media cache, which the copy shares
	sum       []byte
}

// sender sends messages in the order they were written, per chat, with at
// most maxSends in flight. A new message is stored as pending first, so it
// shows at once and survives a crash; the outcome is recorded as a status
// change. A failed message is never sent again on its own: the server may
// have accepted it. The user retries.
type sender struct {
	b    backend.Backend
	p    *pipeline
	sem  chan struct{}
	ctx  context.Context
	stop context.CancelFunc

	mu      sync.Mutex
	queues  map[domain.ChatID][]sendJob
	running map[domain.ChatID]bool
	wg      sync.WaitGroup
}

func newSender(b backend.Backend, p *pipeline) *sender {
	ctx, stop := context.WithCancel(context.Background())
	return &sender{b: b, p: p, sem: make(chan struct{}, maxSends), ctx: ctx, stop: stop,
		queues: map[domain.ChatID][]sendJob{}, running: map[domain.ChatID]bool{}}
}

// enqueue queues a job behind the chat's earlier ones. It does no I/O, so
// the UI can call it from Update and the order is the order of typing.
func (s *sender) enqueue(j sendJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queues[j.chat] = append(s.queues[j.chat], j)
	if !s.running[j.chat] {
		s.running[j.chat] = true
		s.wg.Add(1)
		go s.drain(j.chat)
	}
}

func (s *sender) drain(chat domain.ChatID) {
	defer s.wg.Done()
	defer s.p.guard.protect("sender")
	for {
		s.mu.Lock()
		q := s.queues[chat]
		if len(q) == 0 {
			delete(s.queues, chat)
			delete(s.running, chat)
			s.mu.Unlock()
			return
		}
		j := q[0]
		s.queues[chat] = q[1:]
		s.mu.Unlock()
		s.sem <- struct{}{}
		s.send(j)
		<-s.sem
	}
}

func (s *sender) send(j sendJob) {
	ctx, cancel := context.WithTimeout(s.ctx, sendTimeout)
	defer cancel()
	// Storing the outcome must not depend on the send's deadline.
	store, cancelStore := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelStore()
	c, err := s.p.chatOf(store, j.chat)
	if err != nil {
		s.p.log.Warn("message not sent: no chat", "err", err)
		s.p.ui.Notice("Message not sent: " + err.Error())
		return
	}
	jid, timer := c.JID, c.EphemeralTimer
	me := s.p.opts.MeLID
	if me.IsZero() {
		me = s.p.opts.MePN
	}
	sendErr := s.ctx.Err() // closing: store it as failed, without sending
	if sendErr == nil && j.retry && j.kind.HasMedia() && j.forward == nil {
		sendErr = s.fromCache(store, &j)
	}
	var fwd forwarded
	if j.forward != nil {
		var err error
		if fwd, err = s.forwardOf(store, j.forward); err != nil && !j.retry {
			s.p.ui.Notice("Message not forwarded: " + err.Error())
			return
		}
		sendErr = cmp.Or(sendErr, err)
	}
	in := domain.IncomingMessage{
		ChatJID: jid, SenderJID: me, WAID: j.waid, FromMe: true, Timestamp: j.at, Kind: domain.KindText, Body: j.body,
		QuotedWAID: j.quoted, Mentions: j.mentions, Expiration: timer,
	}
	if timer > 0 {
		in.Flags |= domain.FlagEphemeral
	}
	if j.media != nil {
		a := j.media
		in.Kind = a.kind
		in.Media = &domain.Media{MIME: a.mime, Size: a.size, Width: a.width, Height: a.height}
		if a.kind == domain.KindDocument {
			in.Media.FileName = a.name
		}
	}
	if j.forward != nil {
		in.Kind, in.Body, in.Media, in.Raw = fwd.kind, fwd.body, fwd.media, fwd.raw
		in.Flags |= domain.FlagForwarded
	}
	if !j.retry {
		pending := domain.MessageReceived{Source: domain.SourceSelf, Message: in}
		if err := s.p.writer.SubmitLive(store, []domain.Event{pending}); err != nil {
			s.p.log.Warn("message not saved", "err", err)
			s.p.ui.Notice("Message not saved: " + err.Error())
			return
		}
		if j.media != nil {
			s.keep(store, jid, j)
		}
		if fwd.local != "" {
			_ = s.p.writer.SubmitLive(store, []domain.Event{domain.MediaChanged{ChatJID: jid, TargetWAID: j.waid,
				State: domain.MediaLocal, SHA256: fwd.sum, LocalName: fwd.local}})
		}
	}
	var res backend.SendResult
	started := s.p.opts.Now()
	switch {
	case sendErr != nil:
	case j.forward != nil:
		res, sendErr = s.sendForward(ctx, jid, j, &fwd, timer)
	case j.media != nil:
		var raw []byte
		res, raw, sendErr = s.sendMedia(ctx, jid, j, timer)
		if sendErr == nil && raw != nil {
			// The protocol message, with the media key, for other devices'
			// copies and for a later download.
			in.Raw = raw
			_ = s.p.writer.SubmitLive(store, []domain.Event{domain.MessageReceived{Source: domain.SourceSelf, Message: in}})
		}
	default:
		req := backend.SendRequest{ChatJID: jid, WAID: j.waid, Body: j.body, QuotedWAID: j.quoted, Mentions: j.mentions, Expiration: timer}
		if j.quoted != "" {
			req.QuotedSender, req.QuotedText = s.quote(store, j.chat, j.quoted, me)
		}
		res, sendErr = s.b.SendText(ctx, req)
	}
	// A sent message takes the server's time (MessageSent); the clock
	// learns from it.
	var outcome domain.Event = domain.ReceiptReceived{
		ChatJID: jid, WAIDs: []domain.WAMessageID{j.waid}, From: me, Status: domain.StatusSent, Timestamp: res.Timestamp,
	}
	status := domain.StatusSent
	switch {
	case sendErr != nil:
		status = domain.StatusFailed
		outcome = domain.ReceiptReceived{
			ChatJID: jid, WAIDs: []domain.WAMessageID{j.waid}, From: me, Status: status, Timestamp: s.p.clock.Now(),
		}
	case !res.Timestamp.IsZero():
		s.p.clock.observeSend(started, s.p.opts.Now(), res.Timestamp)
		outcome = domain.MessageSent{ChatJID: jid, WAID: j.waid, At: res.Timestamp}
	}
	if err := s.p.writer.SubmitLive(store, []domain.Event{outcome}); err != nil {
		s.p.log.Warn("message status not saved", "status", status.String(), "err", err)
		s.p.ui.Notice("Message sent, but its status was not saved: " + err.Error())
		return
	}
	if sendErr != nil && s.ctx.Err() == nil {
		s.p.log.Warn("message not sent", "retry", j.retry, "err", sendErr)
		s.p.ui.Notice(fmt.Sprintf("Message not sent: %v. Select it and press ctrl+r to try again", sendErr))
	}
}

// keep puts a copy of an attachment in the media cache, so the sent
// message opens without a download and a retry finds its file.
func (s *sender) keep(ctx context.Context, chat domain.JID, j sendJob) {
	if s.p.media == nil {
		return
	}
	name, sum, err := s.p.media.adopt(j.media.path, j.media.mime)
	if err != nil {
		s.p.log.Info("attachment not cached", "err", err)
		return
	}
	j.media.path = filepath.Join(s.p.media.o.Dir, name)
	_ = s.p.writer.SubmitLive(ctx, []domain.Event{domain.MediaChanged{ChatJID: chat, TargetWAID: j.waid,
		State: domain.MediaLocal, SHA256: sum, LocalName: name}})
}

// fromCache finds the file of a media message sent again.
func (s *sender) fromCache(ctx context.Context, j *sendJob) error {
	src, ok, err := s.p.reader.MediaSource(ctx, j.chat, j.waid)
	var path string
	if err == nil && ok && s.p.media != nil {
		path, ok = s.p.media.o.cachePath(src.LocalName)
	}
	if err != nil || !ok || s.p.media == nil {
		return errors.Join(err, errors.New("its file is no longer kept here: attach it again"))
	}
	j.media = &attachment{path: path, kind: j.kind, mime: src.Media.MIME, name: src.Media.FileName, size: src.Media.Size,
		width: src.Media.Width, height: src.Media.Height}
	return nil
}

// sendMedia uploads and sends an attachment, the encrypted copy going
// through a temporary file in the private cache.
func (s *sender) sendMedia(ctx context.Context, chat domain.JID, j sendJob, timer time.Duration) (backend.SendResult, []byte, error) {
	ms, ok := s.b.(backend.MediaSender)
	if !ok || s.p.media == nil {
		return backend.SendResult{}, nil, errors.New("media cannot be sent here")
	}
	a := j.media
	f, err := os.Open(a.path)
	if err != nil {
		return backend.SendResult{}, nil, err
	}
	defer func() { _ = f.Close() }()
	tmp, err := os.CreateTemp(s.p.media.o.Dir, uploadTemp+"*")
	if err != nil {
		return backend.SendResult{}, nil, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	req := backend.MediaRequest{ChatJID: chat, WAID: j.waid, Kind: a.kind, File: f, Temp: tmp, MIME: a.mime,
		Caption: j.body, Width: a.width, Height: a.height, Expiration: timer}
	if a.kind == domain.KindDocument {
		req.FileName = a.name
	}
	return ms.SendMedia(ctx, req)
}

// forwardOf reads the content a forward copies from its source.
func (s *sender) forwardOf(ctx context.Context, src *forwardSource) (forwarded, error) {
	m, ok, err := s.p.reader.MessageByWAID(ctx, src.chat, src.waid)
	switch {
	case err != nil:
		return forwarded{}, err
	case !ok:
		return forwarded{}, errors.New("the message is no longer stored")
	case !m.Forwardable():
		return forwarded{}, errors.New("this message cannot be forwarded")
	}
	f := forwarded{kind: m.Kind, body: m.Body, forwarded: m.Flags.Has(domain.FlagForwarded)}
	if m.Media != nil {
		media := *m.Media
		media.State = domain.MediaRemote
		f.media = &media
	}
	switch {
	case m.Kind.HasMedia():
		ms, ok, err := s.p.reader.MediaSource(ctx, src.chat, src.waid)
		if err != nil {
			return forwarded{}, err
		}
		if ok {
			f.raw, f.local, f.sum = ms.Raw, ms.LocalName, ms.SHA256
		}
	case m.Kind != domain.KindText:
		if f.raw, err = s.p.reader.Raw(ctx, src.chat, src.waid); err != nil {
			return forwarded{}, err
		}
	}
	if m.Kind != domain.KindText && len(f.raw) == 0 {
		return forwarded{}, errors.New("its content is not stored here")
	}
	return f, nil
}

// sendForward sends a forward. A retry's source is the copy itself, which
// is marked forwarded because it is one; it does not count again.
func (s *sender) sendForward(ctx context.Context, chat domain.JID, j sendJob, f *forwarded, timer time.Duration) (backend.SendResult, error) {
	fw, ok := s.b.(backend.Forwarder)
	if !ok {
		return backend.SendResult{}, errors.New("messages cannot be forwarded here")
	}
	res, _, err := fw.Forward(ctx, backend.ForwardRequest{ChatJID: chat, WAID: j.waid, Kind: f.kind, Body: f.body, Raw: f.raw,
		Forwarded: f.forwarded && !j.retry, Expiration: timer})
	return res, err
}

// quote describes the message a reply quotes: its sender's canonical JID
// (me for this account's own) and its text. A message that is not
// stored gives none; the reply still names it.
func (s *sender) quote(ctx context.Context, chat domain.ChatID, waid domain.WAMessageID, me domain.JID) (domain.JID, string) {
	q, ok, err := s.p.reader.MessageByWAID(ctx, chat, waid)
	if err != nil || !ok {
		return "", ""
	}
	if q.FromMe {
		if me.IsZero() {
			me = s.p.meJID(ctx)
		}
		return me, q.Body
	}
	return s.p.identityJID(ctx, q.SenderID), q.Body
}

// close stops sending. A send in flight is cut off; queued messages are
// stored as failed without being sent, so none is lost and the user can
// retry them. Call it before the writer closes.
func (s *sender) close() {
	s.stop()
	s.wg.Wait()
}
