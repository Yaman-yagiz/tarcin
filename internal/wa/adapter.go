// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// Options configure an Adapter.
type Options struct {
	// DeliverTimeout bounds how long an event handler waits for the
	// store to commit (event-pipeline.md §3.1). An event not committed in
	// time is not acknowledged, and the server delivers it again.
	DeliverTimeout time.Duration
	// HistoryRetry is the first delay before a history sync that could
	// not be downloaded or stored is tried again (2 s); it doubles up to a
	// minute.
	HistoryRetry time.Duration
	// Log receives the adapter's and whatsmeow's log lines; it must
	// redact them (internal/logging). Default: the session's logger.
	Log *slog.Logger
}

// Adapter connects a Session to WhatsApp and implements backend.Backend.
type Adapter struct {
	sess    *Session
	cli     *whatsmeow.Client
	opts    Options
	history *historyQueue
	// stopped holds the terminal connection state once the server ended
	// the session (logged out, replaced, banned, outdated): tarcin then
	// never reconnects on its own.
	stopped atomic.Uint32
	// resyncs are the full app state syncs that recover a collection
	// (see appstate.go); resyncMu guards resyncedAt. fetchAppState is
	// the client's FetchAppState, replaced in tests.
	resyncs       sync.WaitGroup
	resyncMu      sync.Mutex
	resyncedAt    map[appstate.WAPatchName]time.Time
	fetchAppState func(ctx context.Context, name appstate.WAPatchName, fullSync, onlyIfNotSynced bool) error
}

var _ backend.Backend = (*Adapter)(nil)

// maxReconnectDelay caps whatsmeow's reconnect backoff, which otherwise
// grows by two seconds per failed attempt without bound.
const maxReconnectDelay = 30 * time.Second

// New returns an adapter for sess with the client options of ADR-0003:
// acknowledgements wait for the event handlers, so a message is
// persisted before the server learns it was received; decrypted events
// are buffered, so a redelivery stays decryptable; undecryptable
// messages are requested again from the phone; sent messages are kept in
// the session store, so that a recipient's retry request can be answered
// after a restart; and reconnecting backs off by at most 30 seconds.
func New(sess *Session, opts Options) *Adapter {
	if opts.DeliverTimeout <= 0 {
		opts.DeliverTimeout = 5 * time.Second
	}
	if opts.HistoryRetry <= 0 {
		opts.HistoryRetry = 2 * time.Second
	}
	if opts.Log == nil {
		opts.Log = sess.log
	}
	cli := whatsmeow.NewClient(sess.device, newWALogger(opts.Log, "whatsmeow/Client"))
	cli.SynchronousAck = true
	cli.EnableDecryptedEventBuffer = true
	cli.AutomaticMessageRerequestFromPhone = true
	cli.UseRetryMessageStore = true
	cli.ManualHistorySyncDownload = true    // see history.go
	cli.EmitAppStateEventsOnFullSync = true // see appstate.go
	cli.EnableAutoReconnect = true
	cli.InitialAutoReconnect = true
	cli.AutoReconnectHook = func(error) bool {
		capReconnectBackoff(cli)
		return true
	}
	return &Adapter{sess: sess, cli: cli, opts: opts, history: newHistoryQueue(),
		resyncedAt: map[appstate.WAPatchName]time.Time{}, fetchAppState: cli.FetchAppState}
}

// capReconnectBackoff keeps the next reconnect delay, two seconds per
// error so far, at most maxReconnectDelay. It runs on whatsmeow's
// reconnect goroutine, which owns the counter.
func capReconnectBackoff(cli *whatsmeow.Client) {
	if limit := int(maxReconnectDelay / (2 * time.Second)); cli.AutoReconnectErrors > limit {
		cli.AutoReconnectErrors = limit
	}
}

// Run connects and delivers events to sink until ctx ends. A device that
// is not linked yet is linked first (see link.go); if that does not
// complete, Run returns an error wrapping backend.ErrLinkFailed.
func (a *Adapter) Run(ctx context.Context, sink backend.Sink) error {
	// Work that event handlers start in the background ends with Run.
	ctx, stop := context.WithCancel(ctx)
	defer func() { stop(); a.resyncs.Wait() }()
	id := a.cli.AddEventHandlerWithSuccessStatus(func(evt any) bool { return a.handle(ctx, sink, evt) })
	// Never remove the handler from inside a handler: whatsmeow would
	// deadlock.
	defer a.cli.RemoveEventHandler(id)
	var linked <-chan error
	if !a.sess.Paired() {
		items, err := a.cli.GetQRChannel(ctx) // before connecting
		if err != nil {
			return fmt.Errorf("starting to link: %w", err)
		}
		done := make(chan error, 1)
		go func() { done <- a.forwardLink(ctx, sink, items) }()
		linked = done
	}
	a.deliver(ctx, sink, domain.ConnectionChanged{State: domain.ConnConnecting})
	if err := a.cli.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer a.cli.Disconnect()
	// The history worker delivers to sink; it must be done before Run
	// returns and the caller closes the store.
	var history sync.WaitGroup
	history.Go(func() { a.runHistory(ctx, sink) })
	defer history.Wait()
	go func() { _ = watchNetwork(ctx, a.networkChanged) }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-linked:
			if err != nil {
				return err
			}
			linked = nil // linked: keep running
		}
	}
}

// handle converts one whatsmeow event and delivers it. The return value
// tells whatsmeow whether to acknowledge the event: false withholds the
// receipt, so the server delivers it again (G1). A panic counts as a
// failure: whatsmeow recovers it, but would then acknowledge the event.
func (a *Adapter) handle(ctx context.Context, sink backend.Sink, evt any) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			a.opts.Log.Error("event handler panicked; the event is not acknowledged",
				"event", fmt.Sprintf("%T", evt), "panic", panicText(r), "stack", string(debug.Stack()))
			ok = false
		}
	}()
	var ev domain.Event
	switch e := evt.(type) {
	case *events.Connected:
		ev = domain.ConnectionChanged{State: domain.ConnOnline}
	case *events.Disconnected:
		ev = domain.ConnectionChanged{State: domain.ConnDisconnected}
	case *events.LoggedOut:
		ev = domain.ConnectionChanged{State: domain.ConnLoggedOut, Reason: "this device was removed from your account"}
	case *events.StreamReplaced:
		ev = domain.ConnectionChanged{State: domain.ConnStreamReplace, Reason: "another tarcin or WhatsApp Web session took over"}
	case *events.ClientOutdated:
		ev = domain.ConnectionChanged{State: domain.ConnOutdated, Reason: "WhatsApp rejected this version: update tarcin"}
	case *events.TemporaryBan:
		ev = domain.ConnectionChanged{State: domain.ConnBanned, Reason: e.String(), Until: time.Now().Add(e.Expire)}
	case *events.KeepAliveTimeout:
		ev = domain.ConnectionChanged{State: domain.ConnConnecting, Reason: "the connection is not responding"}
	case *events.KeepAliveRestored:
		ev = domain.ConnectionChanged{State: domain.ConnOnline}
	case *events.PairSuccess:
		ev = domain.AccountLinked{PN: fromJID(e.ID), LID: fromJID(e.LID), BusinessName: e.BusinessName}
	case *events.Message:
		if n := e.Message.GetProtocolMessage().GetHistorySyncNotification(); n != nil {
			if !e.Info.IsFromMe {
				a.opts.Log.Warn("ignored a history sync notification from another account")
				return true // only this account's phone sends history; anything else is forged
			}
			return a.queueHistory(ctx, sink, e.Info.ID, n)
		}
		evs := a.normalizeMessage(e, domain.SourceLive)
		if len(evs) > 0 {
			evs = a.resolveMessage(ctx, &e.Info, evs)
		}
		return a.deliverAll(ctx, sink, evs)
	case *events.UndecryptableMessage:
		return a.deliverAll(ctx, sink, normalizeUndecryptable(e))
	case *events.JoinedGroup:
		return a.deliverAll(ctx, sink, normalizeJoinedGroup(e))
	case *events.Receipt:
		return a.deliverAll(ctx, sink, normalizeReceipt(e))
	case *events.Contact:
		return a.deliverAll(ctx, sink, normalizeContact(e))
	case *events.PushName:
		return a.deliverAll(ctx, sink, normalizePushName(e))
	case *events.BusinessName:
		return a.deliverAll(ctx, sink, normalizeBusinessName(e))
	case *events.Pin:
		return a.deliverAll(ctx, sink, normalizePin(e))
	case *events.Mute:
		return a.deliverAll(ctx, sink, normalizeMute(e))
	case *events.Archive:
		return a.deliverAll(ctx, sink, normalizeArchive(e))
	case *events.MarkChatAsRead:
		return a.deliverAll(ctx, sink, normalizeMarkRead(e))
	case *events.DeleteChat:
		return a.deliverAll(ctx, sink, []domain.Event{domain.ChatDeleted{ChatJID: fromJID(e.JID), Before: e.Timestamp}})
	case *events.ClearChat:
		return a.deliverAll(ctx, sink, []domain.Event{domain.ChatCleared{ChatJID: fromJID(e.JID), Before: e.Timestamp}})
	case *events.ChatPresence:
		a.deliverEphemeral(ctx, sink, domain.ChatTyping{ChatJID: chatOf(&e.MessageSource), SenderJID: fromJID(e.Sender),
			Typing: e.State == types.ChatPresenceComposing})
		return true
	case *events.Presence:
		a.deliverEphemeral(ctx, sink, domain.PresenceChanged{JID: fromJID(e.From), Online: !e.Unavailable, LastSeen: e.LastSeen})
		return true
	case *events.GroupInfo:
		return a.deliverAll(ctx, sink, a.normalizeGroupInfo(ctx, e))
	case *events.AppStateSyncError:
		a.recoverAppState(ctx, e)
		return true
	default:
		return true // not handled yet
	}
	switch e := ev.(type) {
	case domain.ConnectionChanged:
		a.opts.Log.Info("connection changed", "state", e.State.String(), "reason", e.Reason)
	case domain.AccountLinked:
		a.opts.Log.Info("device linked")
	}
	if c, ok := ev.(domain.ConnectionChanged); ok {
		switch {
		case c.State.Terminal():
			a.stopped.Store(uint32(c.State))
		case c.State == domain.ConnOnline:
			a.stopped.Store(0)
		}
	}
	return a.deliver(ctx, sink, ev)
}

// queueHistory stores a history sync notification and hands it to the
// worker once it is safe; only then is the notification acknowledged.
func (a *Adapter) queueHistory(ctx context.Context, sink backend.Sink, id types.MessageID, n *waE2E.HistorySyncNotification) bool {
	ev := queued(id, n)
	if ev == nil {
		return true
	}
	if !a.deliver(ctx, sink, ev) {
		return false
	}
	a.history.add(ev.(domain.HistoryQueued))
	return true
}

// deliverAll delivers the events of one whatsmeow event; none means
// there was nothing to store, and the event is acknowledged.
func (a *Adapter) deliverAll(ctx context.Context, sink backend.Sink, evs []domain.Event) bool {
	if len(evs) == 0 {
		return true
	}
	return a.deliver(ctx, sink, evs...)
}

func (a *Adapter) deliver(ctx context.Context, sink backend.Sink, evs ...domain.Event) bool {
	ctx, cancel := context.WithTimeout(ctx, a.opts.DeliverTimeout)
	defer cancel()
	if err := sink.Deliver(ctx, evs); err != nil {
		if !errors.Is(ctx.Err(), context.Canceled) { // not when shutting down
			a.opts.Log.Warn("event not stored; the server will deliver it again",
				"event", fmt.Sprintf("%T", evs[0]), "events", len(evs), "err", err)
		}
		return false
	}
	return true
}

// deliverEphemeral hands over an event that is never stored: there is
// nothing to acknowledge, and losing one is harmless.
func (a *Adapter) deliverEphemeral(ctx context.Context, sink backend.Sink, ev domain.Event) {
	ctx, cancel := context.WithTimeout(ctx, a.opts.DeliverTimeout)
	defer cancel()
	_ = sink.Deliver(ctx, []domain.Event{ev})
}

var _ backend.Presencer = (*Adapter)(nil)

// SetAvailable implements backend.Presencer.
func (a *Adapter) SetAvailable(ctx context.Context, available bool) error {
	if !a.cli.IsConnected() {
		return backend.ErrNotConnected
	}
	state := types.PresenceUnavailable
	if available {
		state = types.PresenceAvailable
	}
	if err := a.cli.SendPresence(ctx, state); err != nil {
		return fmt.Errorf("sending the online status: %w", err)
	}
	return nil
}

// SendTyping implements backend.Presencer.
func (a *Adapter) SendTyping(ctx context.Context, chat domain.JID, composing bool) error {
	c, err := toJID(chat)
	if err != nil {
		return err
	}
	if !a.cli.IsConnected() {
		return backend.ErrNotConnected
	}
	state := types.ChatPresencePaused
	if composing {
		state = types.ChatPresenceComposing
	}
	if err := a.cli.SendChatPresence(ctx, c, state, ""); err != nil {
		return fmt.Errorf("sending the typing state: %w", err)
	}
	return nil
}

// SubscribePresence implements backend.Presencer.
func (a *Adapter) SubscribePresence(ctx context.Context, jid domain.JID) error {
	j, err := toJID(jid)
	if err != nil {
		return err
	}
	if !a.cli.IsConnected() {
		return backend.ErrNotConnected
	}
	if err := a.cli.SubscribePresence(ctx, j); err != nil {
		return fmt.Errorf("following the online status: %w", err)
	}
	return nil
}

var _ backend.Reconnecter = (*Adapter)(nil)

// Reconnect implements backend.Reconnecter: it connects at once instead of
// waiting out whatsmeow's backoff, or restarts a connection that stopped
// answering. A device that was logged out, replaced or banned stays
// disconnected.
func (a *Adapter) Reconnect() {
	if !a.sess.Paired() || a.stopped.Load() == uint32(domain.ConnLoggedOut) {
		return
	}
	if a.cli.IsConnected() {
		a.cli.ResetConnection()
		return
	}
	// The pending automatic attempt finds the client connected and gives
	// way.
	go func() { _ = a.cli.Connect() }()
}

// networkChanged reconnects when the network comes back while tarcin is
// offline, so that it need not wait for the next attempt (budget B10).
func (a *Adapter) networkChanged() {
	if !a.cli.IsConnected() && a.stopped.Load() == 0 {
		a.Reconnect()
	}
}

// Logout unlinks this device from the account: the phone removes it from
// its linked devices, and its keys are deleted from the session store. If
// the phone has removed it already, there is nothing to tell the server.
// Logout connects for this and does not reconnect; Run must not be
// running.
func (a *Adapter) Logout(ctx context.Context) error {
	if !a.sess.Paired() {
		return nil
	}
	a.cli.EnableAutoReconnect = false
	a.cli.InitialAutoReconnect = false
	state := make(chan bool, 1) // true: connected; false: no longer linked
	id := a.cli.AddEventHandler(func(evt any) {
		var connected bool
		switch evt.(type) {
		case *events.Connected:
			connected = true
		case *events.LoggedOut:
		default:
			return
		}
		select {
		case state <- connected:
		default:
		}
	})
	defer a.cli.RemoveEventHandler(id)
	if err := a.cli.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer a.cli.Disconnect()
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting for the server: %w", ctx.Err())
	case connected := <-state:
		if !connected {
			return nil // whatsmeow deleted the keys
		}
	}
	if err := a.cli.Logout(ctx); err != nil {
		return fmt.Errorf("unlinking the device: %w", err)
	}
	return nil
}

var _ backend.GroupFetcher = (*Adapter)(nil)

// FetchGroup implements backend.GroupFetcher with one group info request.
func (a *Adapter) FetchGroup(ctx context.Context, chat domain.JID) ([]domain.Event, error) {
	jid, err := toJID(chat)
	if err != nil {
		return nil, err
	}
	if !a.cli.IsConnected() {
		return nil, backend.ErrNotConnected
	}
	info, err := a.cli.GetGroupInfo(ctx, jid)
	switch {
	case errors.Is(err, whatsmeow.ErrGroupNotFound), errors.Is(err, whatsmeow.ErrNotInGroup):
		return nil, fmt.Errorf("%w: %w", backend.ErrNoGroup, err)
	case err != nil:
		return nil, fmt.Errorf("fetching the group: %w", err)
	}
	return groupEvents(info), nil
}

var _ backend.NumberResolver = (*Adapter)(nil)

// ResolveNumber implements backend.NumberResolver with one contact query
// (IsOnWhatsApp, LID addressing), as the official clients make when a
// number is typed in.
func (a *Adapter) ResolveNumber(ctx context.Context, phone string) (domain.JID, []domain.Event, error) {
	if !a.cli.IsConnected() {
		return "", nil, backend.ErrNotConnected
	}
	res, err := a.cli.IsOnWhatsApp(ctx, []string{"+" + phone})
	if err != nil {
		return "", nil, fmt.Errorf("looking up the number: %w", err)
	}
	return numberResult(res)
}

// numberResult reads the answer to a lookup of one number: the account's
// LID when the server tells it, else its phone-number JID.
func numberResult(res []types.IsOnWhatsAppResponse) (domain.JID, []domain.Event, error) {
	if len(res) == 0 || !res[0].IsIn {
		return "", nil, backend.ErrNotOnWhatsApp
	}
	r := res[0]
	jid, pn := r.JID, r.PhoneNumber
	if pn.IsEmpty() && jid.Server == types.DefaultUserServer {
		pn = jid
	}
	var evs []domain.Event
	if jid.Server == types.HiddenUserServer && !pn.IsEmpty() {
		evs = append(evs, domain.IdentityLinked{PN: fromJID(pn.ToNonAD()), LID: fromJID(jid.ToNonAD())})
	}
	if jid.IsEmpty() {
		jid = pn
	}
	if jid.IsEmpty() {
		return "", nil, backend.ErrNotOnWhatsApp
	}
	return fromJID(jid.ToNonAD()), evs, nil
}

var _ backend.HistoryRequester = (*Adapter)(nil)

// RequestHistory implements backend.HistoryRequester with a peer message
// to this account's phone (BuildHistorySyncRequest). The phone answers
// with an on-demand history sync, which is stored like any other.
func (a *Adapter) RequestHistory(ctx context.Context, chat domain.JID, before backend.HistoryAnchor, count int) error {
	jid, err := toJID(chat)
	if err != nil {
		return err
	}
	if !a.cli.IsConnected() {
		return backend.ErrNotConnected
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{Chat: jid, IsFromMe: before.FromMe},
		ID:            string(before.WAID), Timestamp: before.Timestamp,
	}
	if _, err := a.cli.SendPeerMessage(ctx, a.cli.BuildHistorySyncRequest(info, count)); err != nil {
		return fmt.Errorf("asking the phone for older messages: %w", err)
	}
	return nil
}

// NewMessageID implements backend.Backend.
func (a *Adapter) NewMessageID() domain.WAMessageID {
	return domain.WAMessageID(a.cli.GenerateMessageID())
}

// SendText implements backend.Backend.
func (a *Adapter) SendText(ctx context.Context, req backend.SendRequest) (backend.SendResult, error) {
	to, err := toJID(req.ChatJID)
	if err != nil {
		return backend.SendResult{}, err
	}
	if !a.cli.IsConnected() {
		return backend.SendResult{}, backend.ErrNotConnected
	}
	msg, err := textMessage(&req)
	if err != nil {
		return backend.SendResult{}, err
	}
	resp, err := a.cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{ID: types.MessageID(req.WAID)})
	if err != nil {
		return backend.SendResult{}, fmt.Errorf("sending: %w", err)
	}
	return backend.SendResult{Timestamp: resp.Timestamp}, nil
}

// textMessage builds the protocol message of a text send: plain text, or
// for a reply, extended text whose context names the quoted message, its
// sender and a preview of it.
func textMessage(req *backend.SendRequest) (*waE2E.Message, error) {
	if req.QuotedWAID == "" && len(req.Mentions) == 0 && req.Expiration <= 0 {
		return &waE2E.Message{Conversation: proto.String(req.Body)}, nil
	}
	ctx := &waE2E.ContextInfo{Expiration: expiration(req.Expiration)}
	for _, m := range req.Mentions {
		jid, err := toJID(m)
		if err != nil {
			return nil, fmt.Errorf("a mention: %w", err)
		}
		ctx.MentionedJID = append(ctx.MentionedJID, jid.String())
	}
	if req.QuotedWAID != "" {
		ctx.StanzaID = proto.String(string(req.QuotedWAID))
	}
	if req.QuotedWAID != "" && !req.QuotedSender.IsZero() {
		sender, err := toJID(req.QuotedSender)
		if err != nil {
			return nil, fmt.Errorf("the quoted message's sender: %w", err)
		}
		ctx.Participant = proto.String(sender.String())
	}
	if req.QuotedWAID != "" && req.QuotedText != "" {
		ctx.QuotedMessage = &waE2E.Message{Conversation: proto.String(req.QuotedText)}
	}
	return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(req.Body), ContextInfo: ctx}}, nil
}

var _ backend.Reacter = (*Adapter)(nil)

// SendReaction implements backend.Reacter.
func (a *Adapter) SendReaction(ctx context.Context, chat domain.JID, target domain.WAMessageID, sender domain.JID, emoji string) (time.Time, error) {
	c, err := toJID(chat)
	if err != nil {
		return time.Time{}, err
	}
	var s types.JID
	if !sender.IsZero() {
		if s, err = toJID(sender); err != nil {
			return time.Time{}, err
		}
	}
	if !a.cli.IsConnected() {
		return time.Time{}, backend.ErrNotConnected
	}
	resp, err := a.cli.SendMessage(ctx, c, a.cli.BuildReaction(c, s, types.MessageID(target), emoji))
	if err != nil {
		return time.Time{}, fmt.Errorf("sending the reaction: %w", err)
	}
	return resp.Timestamp, nil
}

var _ backend.Editor = (*Adapter)(nil)

// EditText implements backend.Editor.
func (a *Adapter) EditText(ctx context.Context, chat domain.JID, target domain.WAMessageID, body string) error {
	c, err := toJID(chat)
	if err != nil {
		return err
	}
	if !a.cli.IsConnected() {
		return backend.ErrNotConnected
	}
	msg := a.cli.BuildEdit(c, types.MessageID(target), &waE2E.Message{Conversation: proto.String(body)})
	if _, err := a.cli.SendMessage(ctx, c, msg); err != nil {
		return fmt.Errorf("sending the edit: %w", err)
	}
	return nil
}

// Revoke implements backend.Editor. whatsmeow marks a revoke of someone
// else's message as an admin revoke.
func (a *Adapter) Revoke(ctx context.Context, chat domain.JID, target domain.WAMessageID, sender domain.JID) error {
	c, err := toJID(chat)
	if err != nil {
		return err
	}
	var s types.JID
	if !sender.IsZero() {
		if s, err = toJID(sender); err != nil {
			return err
		}
	}
	if !a.cli.IsConnected() {
		return backend.ErrNotConnected
	}
	if _, err := a.cli.SendMessage(ctx, c, a.cli.BuildRevoke(c, s, types.MessageID(target))); err != nil {
		return fmt.Errorf("deleting the message: %w", err)
	}
	return nil
}

// expiration is a disappearing-message timer for a message's context, nil
// for none.
func expiration(d time.Duration) *uint32 {
	if d <= 0 {
		return nil
	}
	return proto.Uint32(uint32(min(d/time.Second, math.MaxUint32)))
}

// MarkRead implements backend.Backend.
func (a *Adapter) MarkRead(ctx context.Context, chat, sender domain.JID, ids []domain.WAMessageID, at time.Time) error {
	c, err := toJID(chat)
	if err != nil {
		return err
	}
	var s types.JID
	if !sender.IsZero() {
		if s, err = toJID(sender); err != nil {
			return err
		}
	}
	waIDs := make([]types.MessageID, len(ids))
	for i, id := range ids {
		waIDs[i] = types.MessageID(id)
	}
	if err := a.cli.MarkRead(ctx, waIDs, at, c, s); err != nil {
		return fmt.Errorf("sending read receipts: %w", err)
	}
	return nil
}

// panicText describes a recovered panic without its value, which may hold
// message content: a runtime error's text, or the value's type.
func panicText(r any) string {
	if re, ok := r.(runtime.Error); ok {
		return re.Error()
	}
	return fmt.Sprintf("%T", r)
}
