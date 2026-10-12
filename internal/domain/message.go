// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import "time"

// MessageID is the local ID of a stored message. It increases with
// insertion order and breaks ties between messages with equal
// timestamps (WhatsApp timestamps have one-second resolution).
type MessageID int64

// WAMessageID is the WhatsApp ID of a message, unique within a chat.
type WAMessageID string

// MessageKind is the content type of a message.
type MessageKind uint8

// Message kinds.
const (
	KindText MessageKind = iota + 1
	KindImage
	KindVideo
	KindAudio
	KindDocument
	KindSticker
	KindLocation
	KindContact
	KindPoll
	KindSystem      // group events such as "Ali joined"
	KindUnsupported // shown as a placeholder; the raw payload is kept
)

// String implements fmt.Stringer.
func (k MessageKind) String() string {
	names := [...]string{"", "text", "image", "video", "audio", "document", "sticker", "location", "contact", "poll", "system", "unsupported"}
	if int(k) < len(names) && k != 0 {
		return names[k]
	}
	return "unknown"
}

// HasMedia reports whether messages of kind k carry a media attachment.
func (k MessageKind) HasMedia() bool {
	switch k {
	case KindImage, KindVideo, KindAudio, KindDocument, KindSticker:
		return true
	}
	return false
}

// MessageStatus is the delivery state of a message. Apart from Failed,
// the states are ordered and only ever move forward.
type MessageStatus uint8

// Message statuses, in delivery order.
const (
	StatusPending MessageStatus = iota
	StatusSent
	StatusDelivered
	StatusRead
	StatusPlayed
	StatusFailed MessageStatus = 9
)

// String implements fmt.Stringer.
func (s MessageStatus) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusSent:
		return "sent"
	case StatusDelivered:
		return "delivered"
	case StatusRead:
		return "read"
	case StatusPlayed:
		return "played"
	case StatusFailed:
		return "failed"
	}
	return "unknown"
}

// Advance returns the status after observing next. Receipts can arrive
// late, twice or out of order, so a status never moves backwards. A send
// failure only applies to a message that is still pending, and any
// confirmation from the server supersedes a failure.
func (s MessageStatus) Advance(next MessageStatus) MessageStatus {
	switch {
	case next == StatusFailed:
		if s == StatusPending {
			return StatusFailed
		}
		return s
	case s == StatusFailed:
		if next == StatusPending {
			return s
		}
		return next
	case next > s:
		return next
	}
	return s
}

// MessageFlags is a bit set of message properties.
type MessageFlags uint32

// Message flags.
const (
	FlagForwarded MessageFlags = 1 << iota
	FlagViewOnce
	FlagMentionsMe
	FlagEphemeral
)

// Has reports whether all bits of f2 are set in f.
func (f MessageFlags) Has(f2 MessageFlags) bool { return f&f2 == f2 }

// Message is a stored message. Body holds raw, untrusted text.
type Message struct {
	ID         MessageID
	ChatID     ChatID
	WAID       WAMessageID
	SenderID   IdentityID
	FromMe     bool
	Timestamp  time.Time // server time
	Kind       MessageKind
	Body       string // text or caption
	QuotedWAID WAMessageID
	Status     MessageStatus
	EditedAt   time.Time // zero: never edited
	RevokedAt  time.Time // zero: not deleted
	Flags      MessageFlags
	// Unread is set for a message that arrived live from someone else
	// after the chat was last read; history never is.
	Unread bool
	Media  *Media // nil unless Kind.HasMedia()
	// Quote previews the message QuotedWAID names, if it is stored, as
	// it was when this message was read.
	Quote *Quote
	// Reactions counts the reactions to the message by emoji, most
	// frequent first.
	Reactions []ReactionCount
	// Mentions are the people the message mentions, as "@" and the user
	// part of their JID appear in Body.
	Mentions []Mention
	// Revision changes whenever the rendered form of the message may
	// change (edit, revoke, reaction, receipt, media state). The UI uses
	// it as part of its render-cache key.
	Revision uint32
}

// EditWindow is how long after sending a message it can be edited
// (whatsmeow.EditWindow); RevokeWindow is how long it can be deleted for
// everyone. WhatsApp refuses later changes.
const (
	EditWindow   = 20 * time.Minute
	RevokeWindow = 48 * time.Hour
)

// Mention is a person a message mentions.
type Mention struct {
	Token    string     // "@" and the JID's user part, as in the body
	Identity IdentityID // 0 if the JID is not known
}

// ReactionCount is one emoji's reactions to a message.
type ReactionCount struct {
	Emoji string // raw: sanitized when shown
	Count int
	Mine  bool // this account is among them
}

// Quote is what a reply shows of the message it quotes.
type Quote struct {
	SenderID IdentityID
	FromMe   bool
	Kind     MessageKind
	Body     string
	Revoked  bool
}

// Cursor returns the message's position in its chat's history.
func (m *Message) Cursor() Cursor { return Cursor{Time: m.Timestamp, ID: m.ID} }

// IsRevoked reports whether the message was deleted for everyone.
func (m *Message) IsRevoked() bool { return !m.RevokedAt.IsZero() }

// Forwardable reports whether the message's content can be forwarded:
// text, media, a location or a contact that is not deleted or view-once.
func (m *Message) Forwardable() bool {
	if m.WAID == "" || m.IsRevoked() || m.Flags.Has(FlagViewOnce) {
		return false
	}
	switch m.Kind {
	case KindText, KindLocation, KindContact:
		return true
	}
	return m.Kind.HasMedia()
}

// Cursor is a position in a chat's history for keyset pagination. Messages
// are ordered by timestamp and then by local ID.
type Cursor struct {
	Time time.Time
	ID   MessageID
}

// Before reports whether c is strictly older than d.
func (c Cursor) Before(d Cursor) bool {
	if !c.Time.Equal(d.Time) {
		return c.Time.Before(d.Time)
	}
	return c.ID < d.ID
}

// MediaState is the local availability of a media attachment.
type MediaState uint8

// Media states.
const (
	MediaRemote MediaState = iota // only on the server
	MediaDownloading
	MediaLocal
	MediaFailed
	MediaExpired // no longer available on the server
)

// Media describes a message attachment. FileName is untrusted display
// metadata and is never used as a path.
type Media struct {
	MIME     string
	Size     int64
	FileName string
	Width    int
	Height   int
	Duration time.Duration
	State    MediaState
}

// Reaction is one person's emoji reaction to a message. An empty Emoji
// removes the reaction.
type Reaction struct {
	TargetWAID WAMessageID
	SenderID   IdentityID
	Emoji      string
	Timestamp  time.Time
}
