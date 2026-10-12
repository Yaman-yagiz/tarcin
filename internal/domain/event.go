// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import "time"

// Event is a normalized protocol event, produced by a backend and
// consumed by the store writer or the UI bridge. Events carry JIDs, not
// local IDs: resolving JIDs to identities and chats is the writer's job,
// inside its transaction. The set of events is closed.
type Event interface{ isEvent() }

// Source tells where a message event came from.
type Source uint8

// Event sources.
const (
	SourceLive    Source = iota + 1 // delivered in real time
	SourceHistory                   // from a history sync; never notified as new
	SourceSelf                      // sent by this client
)

// IncomingMessage is a message as delivered by the protocol, before
// identity resolution.
type IncomingMessage struct {
	ChatJID    JID
	SenderJID  JID
	SenderAlt  JID // the sender's other address form (PN or LID), if known
	PushName   string
	WAID       WAMessageID
	FromMe     bool
	Timestamp  time.Time
	Kind       MessageKind
	Body       string
	QuotedWAID WAMessageID
	Flags      MessageFlags
	Media      *Media
	// Mentions are the people the message mentions; Body holds "@" and
	// the user part of each.
	Mentions []JID
	// Expiration is the timer of a disappearing message: it disappears
	// that long after Timestamp. Zero: it stays.
	Expiration time.Duration
	// Raw is the serialized protocol message, kept for non-text kinds so
	// media can be downloaded later and new kinds re-parsed.
	Raw []byte
}

// MessageReceived reports a new or redelivered message.
type MessageReceived struct {
	Message IncomingMessage
	Source  Source
}

// ReceiptReceived reports a delivery-state change for messages in a chat.
type ReceiptReceived struct {
	ChatJID   JID
	WAIDs     []WAMessageID
	From      JID
	Status    MessageStatus
	Timestamp time.Time
}

// MessageSent reports that the server accepted one of this account's
// messages and the time it stamped it with. Clients order messages by the
// server's time, so the message moves from the time this computer's clock
// gave it to the server's.
type MessageSent struct {
	ChatJID JID
	WAID    WAMessageID
	At      time.Time
}

// ReactionReceived reports a reaction being set or removed.
type ReactionReceived struct {
	ChatJID    JID
	TargetWAID WAMessageID
	SenderJID  JID
	Emoji      string // empty removes the reaction
	Timestamp  time.Time
}

// MessageEdited reports a new body for an existing message. Only the
// message's sender may edit it.
type MessageEdited struct {
	ChatJID    JID
	TargetWAID WAMessageID
	Body       string
	EditedAt   time.Time
	// Actor is who sent the edit. Empty means tarcin itself, which is
	// trusted.
	Actor JID
}

// MessageRevoked reports a message deleted for everyone. Only the
// message's sender, or a group admin, may delete it.
type MessageRevoked struct {
	ChatJID    JID
	TargetWAID WAMessageID
	RevokedAt  time.Time
	Actor      JID  // who sent the revoke; empty means tarcin itself
	ByAdmin    bool // the revoke claims to come from a group admin
}

// IdentityLinked reports that a phone-number JID and a LID belong to the
// same person, which may require merging identities and chats.
type IdentityLinked struct {
	PN  JID
	LID JID
}

// ContactUpdated reports new names for a person. Empty fields mean
// "unchanged".
type ContactUpdated struct {
	JID          JID
	ContactName  string
	PushName     string
	BusinessName string
}

// ChatFields selects the fields a ChatUpdated event applies.
type ChatFields uint16

// Chat fields.
const (
	ChatFieldKind ChatFields = 1 << iota
	ChatFieldTitle
	ChatFieldPinned
	ChatFieldArchived
	ChatFieldMuted
	ChatFieldEphemeral
	ChatFieldUnread
	ChatFieldHistoryComplete
)

// Has reports whether all bits of f2 are set in f.
func (f ChatFields) Has(f2 ChatFields) bool { return f&f2 == f2 }

// ChatUpdated reports chat metadata from a history sync, app state or a
// group-info change. Only the fields selected by Fields are applied, so
// partial updates never reset other fields.
type ChatUpdated struct {
	ChatJID         JID
	Fields          ChatFields
	Kind            ChatKind
	Title           string
	PinnedAt        time.Time // zero: unpinned
	Archived        bool
	MutedUntil      time.Time // zero: unmuted
	EphemeralTimer  time.Duration
	UnreadCount     int  // from history sync only
	HistoryComplete bool // the phone has no older messages for the chat
}

// GroupRole is a participant's role in a group.
type GroupRole uint8

// Group roles.
const (
	RoleMember GroupRole = iota
	RoleAdmin
	RoleSuperAdmin
)

// GroupMember is a participant of a group.
type GroupMember struct {
	JID    JID
	AltJID JID // the member's other address form, if known
	Role   GroupRole
}

// GroupMetaFields says which fields of a GroupMetaUpdated are set.
type GroupMetaFields uint8

// Group metadata fields.
const (
	GroupFieldTopic GroupMetaFields = 1 << iota
	GroupFieldOwner
	GroupFieldAnnounce
	GroupFieldLocked
)

// GroupMetaUpdated changes a group's description and settings; the
// fields not in Fields stay as they are.
type GroupMetaUpdated struct {
	ChatJID      JID
	Fields       GroupMetaFields
	Topic        string // the description
	Owner        JID
	AnnounceOnly bool // only admins may send
	Locked       bool // only admins may edit the group's info
}

// GroupMembersChanged reports group participants. With Full set, Members
// is the complete list and replaces the stored one; otherwise the members
// are added or updated, and those in Left are removed.
type GroupMembersChanged struct {
	ChatJID JID
	Members []GroupMember
	Left    []JID
	Full    bool
}

// ChatRead reports that the user read a chat up to ReadAt, on this
// device or another one.
type ChatRead struct {
	ChatJID JID
	ReadAt  time.Time
}

// LinkState is a step of linking this device to an account.
type LinkState uint8

// Link states.
const (
	LinkQR          LinkState = iota + 1 // show QR to be scanned on the phone
	LinkPairingCode                      // show Code to be typed on the phone
	LinkPasskey                          // the account needs a passkey: Challenge holds the request
	LinkConfirm                          // ask the user to compare Code with the phone
	LinkFailed                           // linking stopped; Reason says why, and it can be retried
)

// LinkProgress reports a step of linking a device that is not linked yet.
// It is ephemeral: the codes are short-lived and never stored.
type LinkProgress struct {
	State     LinkState
	QR        string        // the QR payload (LinkQR)
	Expires   time.Duration // how long the QR code stays valid
	Code      string        // the pairing or confirmation code
	Challenge string        // WebAuthn request options as JSON (LinkPasskey)
	Reason    string        // LinkFailed
}

// AccountLinked reports that this device is now linked to an account, as
// the identity behind this account's own messages.
type AccountLinked struct {
	PN, LID      JID
	BusinessName string
}

// HistoryQueued records a history sync that the phone made available. It
// is stored before the notification is acknowledged, and the backend
// removes it with HistoryDone once the history is stored: so a sync is
// neither lost to a crash nor deleted from the server before it is safe.
type HistoryQueued struct {
	ID           string // the notification's message ID
	Notification []byte // the backend's notification, to fetch the sync again
}

// HistoryDone reports that a queued history sync is stored, or was given
// up because the server no longer has it.
type HistoryDone struct {
	ID string
}

// SendsInterrupted reports that tarcin starts again: own messages still
// pending from before Before were interrupted and become failed. tarcin
// never sends them again on its own, because the server may have accepted
// them; the user retries.
type SendsInterrupted struct {
	Before time.Time
}

// ChatTyping is an ephemeral typing indicator; it is never persisted.
type ChatTyping struct {
	ChatJID   JID
	SenderJID JID
	Typing    bool
}

// MediaChanged records the local state of a message's media: being
// downloaded, in the cache under LocalName (its content's SHA-256 and an
// extension), failed, or gone from the server.
type MediaChanged struct {
	ChatJID    JID
	TargetWAID WAMessageID
	State      MediaState
	SHA256     []byte // the content's hash, for MediaLocal
	LocalName  string // the cache file's name, for MediaLocal
}

// ChatDeleted deletes a chat's messages up to Before, and the chat itself
// if none is left: the user deleted it here or on another device. A
// message newer than the deletion stays.
type ChatDeleted struct {
	ChatJID JID
	Before  time.Time
}

// ChatCleared deletes a chat's messages up to Before and keeps the chat.
type ChatCleared struct {
	ChatJID JID
	Before  time.Time
}

// MessagesExpired deletes the disappearing messages whose timer ran out
// before Now, as every device of the account does.
type MessagesExpired struct {
	Now time.Time
}

// MediaEvicted reports that a cache file was deleted to keep the cache
// within its size: the media it held is only on the server again.
type MediaEvicted struct {
	LocalName string
}

// PresenceChanged is an ephemeral online status of a person; it is never
// persisted. LastSeen is zero when they hide it.
type PresenceChanged struct {
	JID      JID
	Online   bool
	LastSeen time.Time
}

// ConnectionState is the state of the link to the WhatsApp servers.
type ConnectionState uint8

// Connection states.
const (
	ConnDisconnected ConnectionState = iota
	ConnConnecting
	ConnOnline
	ConnLoggedOut     // device removed; re-pairing needed
	ConnStreamReplace // another client took over this session
	ConnBanned        // temporary ban
	ConnOutdated      // client version rejected; update needed
)

// String implements fmt.Stringer.
func (s ConnectionState) String() string {
	switch s {
	case ConnDisconnected:
		return "disconnected"
	case ConnConnecting:
		return "connecting"
	case ConnOnline:
		return "online"
	case ConnLoggedOut:
		return "logged out"
	case ConnStreamReplace:
		return "replaced by another client"
	case ConnBanned:
		return "temporarily banned"
	case ConnOutdated:
		return "client outdated"
	}
	return "unknown"
}

// Terminal reports whether the state stops automatic reconnection.
func (s ConnectionState) Terminal() bool {
	switch s {
	case ConnLoggedOut, ConnStreamReplace, ConnBanned, ConnOutdated:
		return true
	}
	return false
}

// ConnectionChanged reports a connection state change.
type ConnectionChanged struct {
	State  ConnectionState
	Reason string    // short, user-facing explanation; may be empty
	Until  time.Time // end of a temporary ban, if known
}

func (MessageReceived) isEvent()     {}
func (ReceiptReceived) isEvent()     {}
func (MessageSent) isEvent()         {}
func (ReactionReceived) isEvent()    {}
func (MessageEdited) isEvent()       {}
func (MessageRevoked) isEvent()      {}
func (IdentityLinked) isEvent()      {}
func (ContactUpdated) isEvent()      {}
func (ChatUpdated) isEvent()         {}
func (GroupMembersChanged) isEvent() {}
func (GroupMetaUpdated) isEvent()    {}
func (ChatRead) isEvent()            {}
func (LinkProgress) isEvent()        {}
func (AccountLinked) isEvent()       {}
func (HistoryQueued) isEvent()       {}
func (HistoryDone) isEvent()         {}
func (SendsInterrupted) isEvent()    {}
func (ChatTyping) isEvent()          {}
func (PresenceChanged) isEvent()     {}
func (MediaChanged) isEvent()        {}
func (MediaEvicted) isEvent()        {}
func (MessagesExpired) isEvent()     {}
func (ChatDeleted) isEvent()         {}
func (ChatCleared) isEvent()         {}
func (ConnectionChanged) isEvent()   {}
