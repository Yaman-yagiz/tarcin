// SPDX-License-Identifier: GPL-3.0-or-later

// Package fake is a deterministic, in-process implementation of
// backend.Backend. It simulates an account with direct chats and groups,
// an initial sync, a history sync and live traffic at a configurable rate,
// and it can inject the faults the pipeline must survive: duplicate and
// reordered delivery, redelivery after a rejected batch, connection flaps
// and failed sends. It never touches the network.
package fake

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/devtools/synth"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// ErrSendFailed is returned by SendText when a failure is injected.
var ErrSendFailed = errors.New("fake: send failed (injected)")

// Config controls the simulated account and its traffic. The zero value
// is a small, quiet account.
type Config struct {
	Seed            uint64
	Chats           int     // number of chats (default 20)
	GroupRatio      float64 // share of chats that are groups (default 0.4)
	MembersPerGroup int     // participants per group, excluding me (default 30)
	// HotChatMembers, if set, makes chat 0 a group of this size that
	// receives HotChatShare of the live traffic (a busy group).
	HotChatMembers int
	HotChatShare   float64
	// StressNames gives the busy group's members display names that
	// stress width computation and truncation: very long, emoji,
	// right-to-left (scenario S4).
	StressNames bool

	InitialHistory int           // history messages per chat delivered at start
	HotChatHistory int           // if set, chat 0 gets this many history messages instead
	HistoryChunk   int           // messages per history batch (default 500)
	HistorySpan    time.Duration // history timestamps span this period (default 30 days)
	// OnDemandHistory is how many messages older than its history each
	// chat still has on the "phone", for RequestHistory; after them, the
	// chat's history is complete.
	OnDemandHistory int

	Rate      float64 // live messages per second (0: none)
	MaxEvents int     // stop after this many live messages (0: unlimited)

	DuplicateRate    float64 // probability a live message is delivered twice
	ReorderWindow    int     // deliver live messages shuffled within windows of this size
	TypingRate       float64 // probability a typing indicator precedes a message
	PNAddressingRate float64 // probability a message addresses people by phone number

	FlapEvery    time.Duration // simulate a disconnect this often (0: never)
	FlapDowntime time.Duration // how long a flap lasts (default 2s)

	SendLatency     time.Duration
	DownloadLatency time.Duration // how long a media download takes
	SendFailRate    float64
	AutoReceipts    bool          // emit delivered and read receipts for sent messages
	RedeliverDelay  time.Duration // retry delay after the sink rejects a batch (default 1s)

	Script []Scripted // events delivered at fixed offsets from the start of Run

	// Link starts Run as a device that is not linked yet: it shows QR
	// codes until the phone "scans" one after LinkAfter, or until
	// PairWithPhone was used, and then delivers AccountLinked. With
	// LinkPasskey the account asks for a passkey and a confirmation first.
	Link          bool
	LinkAfter     time.Duration // default 8s
	LinkCodeEvery time.Duration // how often the QR code changes (default 20s)
	LinkPasskey   bool

	Tick  time.Duration    // scheduling granularity (default 10ms)
	Clock func() time.Time // default time.Now
}

// Scripted is a batch delivered At after Run starts.
type Scripted struct {
	At     time.Duration
	Events []domain.Event
}

// Person is a simulated contact.
type Person struct {
	LID  domain.JID
	PN   domain.JID
	Name string
}

// Chat is a simulated conversation.
type Chat struct {
	JID     domain.JID // group JID, or the peer's LID
	Kind    domain.ChatKind
	Title   string   // groups only
	Peer    Person   // direct chats only
	Members []Person // groups only, excluding me
}

var _ backend.Backend = (*Backend)(nil)

// Backend is the fake backend. Create it with New.
type Backend struct {
	cfg   Config
	me    Person
	chats []Chat

	online    atomic.Bool
	downloads atomic.Int32
	linker    chan string   // what the user did on the link screen
	reconnect chan struct{} // ends a simulated flap early
	older     chan olderRequest
	presence  chan domain.JID // subscriptions for Run to answer

	mu       sync.Mutex // guards the fields below
	ids      *synth.Gen
	faults   *rand.Rand
	sent     []backend.SendRequest
	read     []domain.WAMessageID
	fetched  []domain.JID
	reacted  []domain.ReactionReceived
	media    []backend.MediaRequest
	forwards []backend.ForwardRequest
	settings []Setting
	edits    []domain.MessageEdited
	revokes  []domain.MessageRevoked
	typing   []bool
	avail    []bool
	receipts []timed
}

type timed struct {
	due    time.Time
	events []domain.Event
}

// New creates a fake backend. The chat directory is generated from
// cfg.Seed, so the same configuration always describes the same account.
func New(cfg Config) *Backend {
	cfg = withDefaults(cfg)
	g := synth.New(cfg.Seed)
	b := &Backend{
		cfg:       cfg,
		me:        Person{LID: g.LID(), PN: g.PN(), Name: "Me"},
		ids:       synth.New(cfg.Seed ^ 0x5eed),
		faults:    rand.New(rand.NewPCG(cfg.Seed, 0xfa17)), //nolint:gosec // fault injection, not security-sensitive
		linker:    make(chan string, 4),
		reconnect: make(chan struct{}, 1),
		older:     make(chan olderRequest, 16),
		presence:  make(chan domain.JID, 16),
	}
	person := func() Person { return Person{LID: g.LID(), PN: g.PN(), Name: g.Name()} }
	groups := int(float64(cfg.Chats)*cfg.GroupRatio + 0.5)
	for i := range cfg.Chats {
		switch {
		case i == 0 && cfg.HotChatMembers > 0:
			member := person
			if cfg.StressNames {
				member = func() Person { return Person{LID: g.LID(), PN: g.PN(), Name: g.StressName()} }
			}
			b.chats = append(b.chats, b.group(g, cfg.HotChatMembers, member))
		case i < groups:
			b.chats = append(b.chats, b.group(g, cfg.MembersPerGroup, person))
		default:
			p := person()
			b.chats = append(b.chats, Chat{JID: p.LID, Kind: domain.ChatDirect, Peer: p})
		}
	}
	return b
}

func (b *Backend) group(g *synth.Gen, members int, person func() Person) Chat {
	c := Chat{JID: g.GroupJID(), Kind: domain.ChatGroup, Title: g.GroupTitle()}
	for range members {
		c.Members = append(c.Members, person())
	}
	return c
}

func withDefaults(cfg Config) Config {
	if cfg.Chats <= 0 {
		cfg.Chats = 20
	}
	if cfg.GroupRatio == 0 {
		cfg.GroupRatio = 0.4
	}
	if cfg.MembersPerGroup <= 0 {
		cfg.MembersPerGroup = 30
	}
	if cfg.HistoryChunk <= 0 {
		cfg.HistoryChunk = 500
	}
	if cfg.HistorySpan <= 0 {
		cfg.HistorySpan = 30 * 24 * time.Hour
	}
	if cfg.FlapDowntime <= 0 {
		cfg.FlapDowntime = 2 * time.Second
	}
	if cfg.RedeliverDelay <= 0 {
		cfg.RedeliverDelay = time.Second
	}
	if cfg.Tick <= 0 {
		cfg.Tick = 10 * time.Millisecond
	}
	if cfg.LinkAfter <= 0 {
		cfg.LinkAfter = 8 * time.Second
	}
	if cfg.LinkCodeEvery <= 0 {
		cfg.LinkCodeEvery = 20 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return cfg
}

// Me returns the simulated account's own identity.
func (b *Backend) Me() Person { return b.me }

// Chats returns the simulated chat directory.
func (b *Backend) Chats() []Chat {
	out := make([]Chat, len(b.chats))
	copy(out, b.chats)
	return out
}

// Sent returns the messages sent so far.
func (b *Backend) Sent() []backend.SendRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]backend.SendRequest, len(b.sent))
	copy(out, b.sent)
	return out
}

// MarkedRead returns the message IDs marked as read so far.
func (b *Backend) MarkedRead() []domain.WAMessageID {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]domain.WAMessageID, len(b.read))
	copy(out, b.read)
	return out
}

// NewMessageID implements backend.Backend.
func (b *Backend) NewMessageID() domain.WAMessageID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ids.WAID()
}

// SendText implements backend.Backend.
func (b *Backend) SendText(ctx context.Context, req backend.SendRequest) (backend.SendResult, error) {
	if !b.online.Load() {
		return backend.SendResult{}, backend.ErrNotConnected
	}
	if d := b.cfg.SendLatency; d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return backend.SendResult{}, ctx.Err()
		case <-t.C:
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.faults.Float64() < b.cfg.SendFailRate {
		return backend.SendResult{}, ErrSendFailed
	}
	now := b.cfg.Clock().Truncate(time.Second)
	b.sent = append(b.sent, req)
	if b.cfg.AutoReceipts {
		receipt := func(s domain.MessageStatus) []domain.Event {
			return []domain.Event{domain.ReceiptReceived{
				ChatJID: req.ChatJID, WAIDs: []domain.WAMessageID{req.WAID}, Status: s, Timestamp: now,
			}}
		}
		b.receipts = append(b.receipts,
			timed{due: now.Add(300 * time.Millisecond), events: receipt(domain.StatusDelivered)},
			timed{due: now.Add(1500 * time.Millisecond), events: receipt(domain.StatusRead)})
	}
	return backend.SendResult{Timestamp: now}, nil
}

// MarkRead implements backend.Backend.
func (b *Backend) MarkRead(_ context.Context, _, _ domain.JID, ids []domain.WAMessageID, _ time.Time) error {
	if !b.online.Load() {
		return backend.ErrNotConnected
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.read = append(b.read, ids...)
	return nil
}

var _ backend.GroupFetcher = (*Backend)(nil)

// FetchGroup implements backend.GroupFetcher from the chat directory,
// after SendLatency.
func (b *Backend) FetchGroup(ctx context.Context, chat domain.JID) ([]domain.Event, error) {
	if !b.online.Load() {
		return nil, backend.ErrNotConnected
	}
	if d := b.cfg.SendLatency; d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	b.mu.Lock()
	b.fetched = append(b.fetched, chat)
	b.mu.Unlock()
	for i := range b.chats {
		if c := &b.chats[i]; c.JID == chat && c.Kind == domain.ChatGroup {
			meta := domain.GroupMetaUpdated{ChatJID: c.JID, Fields: domain.GroupFieldTopic,
				Topic: "A simulated group of " + strconv.Itoa(len(c.Members)+1)}
			if len(c.Members) > 0 {
				meta.Fields |= domain.GroupFieldOwner
				meta.Owner = c.Members[0].LID
			}
			return []domain.Event{
				domain.ChatUpdated{ChatJID: c.JID, Fields: domain.ChatFieldTitle, Title: c.Title},
				meta,
				b.groupMembers(c),
			}, nil
		}
	}
	return nil, backend.ErrNoGroup
}

// olderRequest is a RequestHistory call for the Run loop.
type olderRequest struct {
	chat   domain.JID
	before time.Time
	count  int
}

var _ backend.HistoryRequester = (*Backend)(nil)

// ErrBusy is returned by RequestHistory when too many requests wait.
var ErrBusy = errors.New("fake: too many history requests")

// RequestHistory implements backend.HistoryRequester: Run answers with up
// to count older messages, as history, from the chat's OnDemandHistory.
func (b *Backend) RequestHistory(_ context.Context, chat domain.JID, before backend.HistoryAnchor, count int) error {
	if !b.online.Load() {
		return backend.ErrNotConnected
	}
	select {
	case b.older <- olderRequest{chat: chat, before: before.Timestamp, count: count}:
		return nil
	default:
		return ErrBusy
	}
}

var _ backend.Reacter = (*Backend)(nil)

// SendReaction implements backend.Reacter by recording the reaction.
func (b *Backend) SendReaction(_ context.Context, chat domain.JID, target domain.WAMessageID, _ domain.JID, emoji string) (time.Time, error) {
	if !b.online.Load() {
		return time.Time{}, backend.ErrNotConnected
	}
	now := b.cfg.Clock().Truncate(time.Second)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reacted = append(b.reacted, domain.ReactionReceived{ChatJID: chat, TargetWAID: target, SenderJID: b.me.LID, Emoji: emoji, Timestamp: now})
	return now, nil
}

var _ backend.Editor = (*Backend)(nil)

// EditText implements backend.Editor by recording the edit.
func (b *Backend) EditText(_ context.Context, chat domain.JID, target domain.WAMessageID, body string) error {
	if !b.online.Load() {
		return backend.ErrNotConnected
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.edits = append(b.edits, domain.MessageEdited{ChatJID: chat, TargetWAID: target, Body: body})
	return nil
}

// Revoke implements backend.Editor by recording the revoke.
func (b *Backend) Revoke(_ context.Context, chat domain.JID, target domain.WAMessageID, sender domain.JID) error {
	if !b.online.Load() {
		return backend.ErrNotConnected
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.revokes = append(b.revokes, domain.MessageRevoked{ChatJID: chat, TargetWAID: target, Actor: sender, ByAdmin: !sender.IsZero()})
	return nil
}

// Edited and Revoked return the edits and revokes sent so far.
func (b *Backend) Edited() []domain.MessageEdited {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.edits)
}

// Revoked returns the revokes sent so far; Actor holds the sender of
// someone else's message.
func (b *Backend) Revoked() []domain.MessageRevoked {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.revokes)
}

var _ backend.Presencer = (*Backend)(nil)

// SetAvailable implements backend.Presencer by recording the status.
func (b *Backend) SetAvailable(_ context.Context, available bool) error {
	if !b.online.Load() {
		return backend.ErrNotConnected
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.avail = append(b.avail, available)
	return nil
}

// SendTyping implements backend.Presencer by recording the state.
func (b *Backend) SendTyping(_ context.Context, _ domain.JID, composing bool) error {
	if !b.online.Load() {
		return backend.ErrNotConnected
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.typing = append(b.typing, composing)
	return nil
}

// SubscribePresence implements backend.Presencer: Run answers that the
// person is online.
func (b *Backend) SubscribePresence(_ context.Context, jid domain.JID) error {
	if !b.online.Load() {
		return backend.ErrNotConnected
	}
	select {
	case b.presence <- jid:
		return nil
	default:
		return ErrBusy
	}
}

var _ backend.MediaDownloader = (*Backend)(nil)

// fakeMedia is what every fake download holds.
var fakeMedia = []byte("fake media from the tarcin demo\n")

// DownloadMedia implements backend.MediaDownloader with a few fixed bytes,
// after DownloadLatency.
func (b *Backend) DownloadMedia(ctx context.Context, _ []byte, f *os.File, limit int64) error {
	if !b.online.Load() {
		return backend.ErrNotConnected
	}
	if d := b.cfg.DownloadLatency; d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	if int64(len(fakeMedia)) > limit {
		return backend.ErrMediaTooLarge
	}
	b.downloads.Add(1)
	_, err := f.Write(fakeMedia)
	return err
}

var _ backend.MediaSender = (*Backend)(nil)

// SendMedia implements backend.MediaSender by reading the file and
// recording the request, without the files.
func (b *Backend) SendMedia(ctx context.Context, req backend.MediaRequest) (backend.SendResult, []byte, error) {
	if !b.online.Load() {
		return backend.SendResult{}, nil, backend.ErrNotConnected
	}
	if _, err := io.Copy(io.Discard, req.File); err != nil {
		return backend.SendResult{}, nil, err
	}
	res, err := b.SendText(ctx, backend.SendRequest{ChatJID: req.ChatJID, WAID: req.WAID, Body: req.Caption})
	if err != nil {
		return res, nil, err
	}
	req.File, req.Temp = nil, nil
	b.mu.Lock()
	b.media = append(b.media, req)
	b.mu.Unlock()
	return res, []byte("fake media message"), nil
}

var _ backend.Forwarder = (*Backend)(nil)

// Forward implements backend.Forwarder like SendText, recording the
// request; the copy's protocol message is the original's.
func (b *Backend) Forward(ctx context.Context, req backend.ForwardRequest) (backend.SendResult, []byte, error) {
	res, err := b.SendText(ctx, backend.SendRequest{ChatJID: req.ChatJID, WAID: req.WAID, Body: req.Body})
	if err != nil {
		return res, nil, err
	}
	b.mu.Lock()
	b.forwards = append(b.forwards, req)
	b.mu.Unlock()
	return res, req.Raw, nil
}

// Forwarded returns the messages forwarded so far.
func (b *Backend) Forwarded() []backend.ForwardRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.forwards)
}

// SentMedia returns the attachments sent so far.
func (b *Backend) SentMedia() []backend.MediaRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.media)
}

var _ backend.ChatSettings = (*Backend)(nil)

// Setting is a chat setting change the fake recorded.
type Setting struct {
	Chat  domain.JID
	What  string // "mute", "pin" or "archive"
	On    bool
	Until time.Time // mutes
}

func (b *Backend) setting(s Setting) error {
	if !b.online.Load() {
		return backend.ErrNotConnected
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settings = append(b.settings, s)
	return nil
}

// MuteChat implements backend.ChatSettings by recording the change.
func (b *Backend) MuteChat(_ context.Context, chat domain.JID, until time.Time) error {
	return b.setting(Setting{Chat: chat, What: "mute", On: !until.IsZero(), Until: until})
}

// PinChat implements backend.ChatSettings by recording the change.
func (b *Backend) PinChat(_ context.Context, chat domain.JID, pinned bool) error {
	return b.setting(Setting{Chat: chat, What: "pin", On: pinned})
}

// ArchiveChat implements backend.ChatSettings by recording the change.
func (b *Backend) ArchiveChat(_ context.Context, chat domain.JID, archived bool) error {
	return b.setting(Setting{Chat: chat, What: "archive", On: archived})
}

// Settings returns the chat setting changes so far.
func (b *Backend) Settings() []Setting {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.settings)
}

// Downloads returns how many media downloads were made.
func (b *Backend) Downloads() int { return int(b.downloads.Load()) }

// Presence returns the online statuses and typing states sent so far.
func (b *Backend) Presence() (available, typing []bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.avail), slices.Clone(b.typing)
}

// Reacted returns the reactions sent so far.
func (b *Backend) Reacted() []domain.ReactionReceived {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.reacted)
}

// FetchedGroups returns the groups fetched so far, in order.
func (b *Backend) FetchedGroups() []domain.JID {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.fetched)
}

// groupMembers lists a group in full: me, and its members, of whom the
// first is the owner and the second an admin.
func (b *Backend) groupMembers(c *Chat) domain.GroupMembersChanged {
	members := make([]domain.GroupMember, 0, len(c.Members)+1)
	members = append(members, domain.GroupMember{JID: b.me.LID, AltJID: b.me.PN})
	for i, m := range c.Members {
		role := domain.RoleMember
		switch i {
		case 0:
			role = domain.RoleSuperAdmin
		case 1:
			role = domain.RoleAdmin
		}
		members = append(members, domain.GroupMember{JID: m.LID, AltJID: m.PN, Role: role})
	}
	return domain.GroupMembersChanged{ChatJID: c.JID, Members: members, Full: true}
}

// takeReceipts removes and returns the receipt batches due at now.
func (b *Backend) takeReceipts(now time.Time) [][]domain.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var due [][]domain.Event
	keep := b.receipts[:0]
	for _, r := range b.receipts {
		if r.due.After(now) {
			keep = append(keep, r)
			continue
		}
		due = append(due, r.events)
	}
	b.receipts = keep
	return due
}

func (b *Backend) pendingReceipts() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.receipts) > 0
}
