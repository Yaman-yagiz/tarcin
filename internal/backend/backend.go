// SPDX-License-Identifier: GPL-3.0-or-later

// Package backend defines the protocol port: the interface between tarcin
// and a messaging protocol implementation. internal/wa implements it with
// whatsmeow; internal/backend/fake implements it for tests, benchmarks and
// the --demo mode. Only domain types cross this boundary.
package backend

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// ErrNotConnected is returned by operations that need a live connection.
var ErrNotConnected = errors.New("backend: not connected")

// ErrLinkFailed is returned by Run when linking a new device stopped: the
// codes expired or the phone refused. Run can be called again for new
// codes.
var ErrLinkFailed = errors.New("backend: the device was not linked")

// Sink receives the events a backend produces.
//
// Deliver hands over a batch of events. For durable events (see Durable)
// it must return only after the whole batch is committed to storage. A
// non-nil error means nothing was committed: the backend must not
// acknowledge the events, so that the server redelivers them (guarantee
// G1 in docs/architecture/event-pipeline.md). Ephemeral events are never
// persisted. Deliver must be safe for concurrent use and must honor ctx.
type Sink interface {
	Deliver(ctx context.Context, events []domain.Event) error
}

// SinkFunc adapts a function to the Sink interface.
type SinkFunc func(ctx context.Context, events []domain.Event) error

// Deliver implements Sink.
func (f SinkFunc) Deliver(ctx context.Context, events []domain.Event) error { return f(ctx, events) }

// SendRequest describes an outgoing text message.
type SendRequest struct {
	ChatJID    domain.JID
	WAID       domain.WAMessageID // generated with NewMessageID before sending
	Body       string
	QuotedWAID domain.WAMessageID // empty unless replying
	// QuotedSender and QuotedText describe the quoted message, for the
	// preview recipients see; empty if it is not stored.
	QuotedSender domain.JID
	QuotedText   string
	// Mentions are the people Body mentions, each as "@" and the user
	// part of its JID.
	Mentions []domain.JID
	// Expiration is the chat's disappearing-message timer, if it has one:
	// the message disappears that long after it is sent.
	Expiration time.Duration
}

// SendResult is the server's answer to a send.
type SendResult struct {
	Timestamp time.Time // server timestamp of the sent message
}

// Backend is a messaging protocol implementation.
type Backend interface {
	// Run connects and delivers events to sink until ctx is canceled or
	// a terminal connection state is reached. It returns nil when ctx is
	// canceled.
	Run(ctx context.Context, sink Sink) error

	// NewMessageID returns a fresh ID for an outgoing message.
	NewMessageID() domain.WAMessageID

	// SendText sends a text message.
	SendText(ctx context.Context, req SendRequest) (SendResult, error)

	// MarkRead sends read receipts for messages of sender in chat.
	MarkRead(ctx context.Context, chat, sender domain.JID, ids []domain.WAMessageID, at time.Time) error
}

// Linker is implemented by backends that can link a new device to an
// account. While a device is not linked yet, Run delivers LinkProgress
// events (a QR code first) and AccountLinked once the phone has approved
// the link; these methods drive the other ways of linking.
type Linker interface {
	// PairWithPhone switches to a code that the user types on the phone
	// and returns the code. phone is the account's number in
	// international format.
	PairWithPhone(ctx context.Context, phone string) (string, error)
	// SubmitPasskey answers a LinkPasskey challenge with the
	// authenticator's response (WebAuthn JSON).
	SubmitPasskey(ctx context.Context, response string) error
	// ConfirmLink confirms that the code of a LinkConfirm step matches
	// the one on the phone.
	ConfirmLink(ctx context.Context) error
}

// ErrNoGroup is returned for a group that does not exist, or that this
// account is no longer a member of. Asking again does not help.
var ErrNoGroup = errors.New("backend: no such group, or not a member")

// GroupFetcher is implemented by backends that can ask the server for a
// group's current state. Callers rate-limit it (internal/jobs) and never
// fetch every group at once.
type GroupFetcher interface {
	// FetchGroup returns events that describe the group in full: its
	// title, description and settings, and the complete member list.
	FetchGroup(ctx context.Context, chat domain.JID) ([]domain.Event, error)
}

// HistoryAnchor is the oldest stored message of a chat, before which
// older messages are asked for.
type HistoryAnchor struct {
	WAID      domain.WAMessageID
	FromMe    bool
	Timestamp time.Time
}

// ErrNotOnWhatsApp is returned for a phone number that has no WhatsApp
// account.
var ErrNotOnWhatsApp = errors.New("backend: this number is not on WhatsApp")

// NumberResolver is implemented by backends that can look up who uses a
// phone number, to start a chat with someone not in any chat yet. It is
// called once per number the user types, never in bulk.
type NumberResolver interface {
	// ResolveNumber returns the address of the account that uses phone,
	// in international format without the "+", and events that link its
	// address forms (IdentityLinked). It returns ErrNotOnWhatsApp if no
	// account uses the number.
	ResolveNumber(ctx context.Context, phone string) (domain.JID, []domain.Event, error)
}

// HistoryRequester is implemented by backends that can ask the phone for
// a chat's older messages.
type HistoryRequester interface {
	// RequestHistory asks for up to count messages of chat older than
	// before, and returns once the request is sent. The messages arrive
	// later, as history events, as does a ChatUpdated with
	// HistoryComplete when the phone has none older.
	RequestHistory(ctx context.Context, chat domain.JID, before HistoryAnchor, count int) error
}

// Reacter is implemented by backends that can react to messages.
type Reacter interface {
	// SendReaction reacts to the message target of chat, written by
	// sender (empty: this account), with emoji; an empty emoji removes
	// this account's reaction. It returns the server's time.
	SendReaction(ctx context.Context, chat domain.JID, target domain.WAMessageID, sender domain.JID, emoji string) (time.Time, error)
}

// Editor is implemented by backends that can change sent messages.
type Editor interface {
	// EditText replaces the text of this account's message target.
	EditText(ctx context.Context, chat domain.JID, target domain.WAMessageID, body string) error
	// Revoke deletes the message target for everyone: this account's own
	// (sender empty) or, as a group admin, sender's.
	Revoke(ctx context.Context, chat domain.JID, target domain.WAMessageID, sender domain.JID) error
}

// Presencer is implemented by backends that send and follow online
// status and typing.
type Presencer interface {
	// SetAvailable tells contacts whether this account is online.
	SetAvailable(ctx context.Context, available bool) error
	// SendTyping tells a chat whether this account is typing.
	SendTyping(ctx context.Context, chat domain.JID, composing bool) error
	// SubscribePresence asks to be told a person's online status.
	SubscribePresence(ctx context.Context, jid domain.JID) error
}

var (
	// ErrMediaGone is returned for media the server no longer has.
	ErrMediaGone = errors.New("backend: the media is no longer on the server")
	// ErrMediaTooLarge is returned for media larger than the limit.
	ErrMediaTooLarge = errors.New("backend: the media is larger than the download limit")
)

// MediaDownloader is implemented by backends that can download media.
type MediaDownloader interface {
	// DownloadMedia decrypts the media of a stored message (raw: its
	// protocol message) into f, which it may write, seek and truncate. It
	// refuses media declared or found larger than limit bytes.
	DownloadMedia(ctx context.Context, raw []byte, f *os.File, limit int64) error
}

// MediaRequest is an attachment to send.
type MediaRequest struct {
	ChatJID  domain.JID
	WAID     domain.WAMessageID
	Kind     domain.MessageKind // image, video, audio or document
	File     *os.File           // the attachment, read from its start
	Temp     *os.File           // scratch space for the encrypted upload
	MIME     string
	FileName string // documents
	Caption  string
	Width    int // images and videos, if known
	Height   int
	Duration time.Duration // audio and video, if known
	// Expiration is the chat's disappearing-message timer, if it has one.
	Expiration time.Duration
}

// MediaSender is implemented by backends that can send attachments.
type MediaSender interface {
	// SendMedia uploads and sends an attachment. It returns the protocol
	// message it sent, to be stored like a received one.
	SendMedia(ctx context.Context, req MediaRequest) (res SendResult, raw []byte, err error)
}

// ForwardRequest forwards a stored message to a chat.
type ForwardRequest struct {
	ChatJID domain.JID
	WAID    domain.WAMessageID // the copy's ID, from NewMessageID
	Kind    domain.MessageKind
	Body    string // the text of a text message
	// Raw is the stored protocol message of any other kind. Its media
	// keeps its location and key, so nothing is uploaded again.
	Raw []byte
	// Forwarded reports that a text message was itself forwarded; other
	// kinds carry how often in Raw.
	Forwarded bool
	// Expiration is the chat's disappearing-message timer, if it has one.
	Expiration time.Duration
}

// Forwarder is implemented by backends that can forward messages.
type Forwarder interface {
	// Forward sends a copy of a message, marked as forwarded. It returns
	// the protocol message it sent, to be stored like a received one.
	Forward(ctx context.Context, req ForwardRequest) (res SendResult, raw []byte, err error)
}

// ChatSettings is implemented by backends that can change a chat's
// settings, which the account's devices share (WhatsApp's app state).
type ChatSettings interface {
	// MuteChat mutes a chat until a time, domain.MuteForever for always,
	// or unmutes it with the zero time.
	MuteChat(ctx context.Context, chat domain.JID, until time.Time) error
	// PinChat pins or unpins a chat.
	PinChat(ctx context.Context, chat domain.JID, pinned bool) error
	// ArchiveChat archives a chat, which also unpins it, or unarchives it.
	ArchiveChat(ctx context.Context, chat domain.JID, archived bool) error
}

// Reconnecter is implemented by backends that can connect again at once,
// for the :reconnect command and after the network changes.
type Reconnecter interface {
	Reconnect()
}

// Durable reports whether ev must be persisted before it is acknowledged.
func Durable(ev domain.Event) bool {
	switch ev.(type) {
	case domain.ChatTyping, domain.PresenceChanged, domain.ConnectionChanged, domain.LinkProgress:
		return false
	}
	return true
}
