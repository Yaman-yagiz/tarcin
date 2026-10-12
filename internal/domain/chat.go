// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import "time"

// ChatID is the local, stable ID of a conversation (a row in chats).
type ChatID int64

// ChatKind distinguishes direct chats from groups and other channels.
type ChatKind uint8

// Chat kinds.
const (
	ChatDirect ChatKind = iota + 1
	ChatGroup
	ChatBroadcast
	ChatNewsletter
)

// String implements fmt.Stringer.
func (k ChatKind) String() string {
	switch k {
	case ChatDirect:
		return "direct"
	case ChatGroup:
		return "group"
	case ChatBroadcast:
		return "broadcast"
	case ChatNewsletter:
		return "newsletter"
	}
	return "unknown"
}

// MuteForever is the MutedUntil value of a chat muted without an end.
var MuteForever = time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)

// Chat is a conversation: a direct chat, a group, a broadcast list or a
// channel. The chat list is built from chats, never from contacts.
type Chat struct {
	ID              ChatID
	JID             JID // group JID, or the peer's canonical JID
	Kind            ChatKind
	PeerID          IdentityID // direct chats only
	Title           string     // group subject; direct chats use the peer's name
	LastActivity    time.Time
	LastMessageID   MessageID
	UnreadCount     int
	LastReadAt      time.Time
	PinnedAt        time.Time // zero: not pinned
	Archived        bool
	MutedUntil      time.Time     // zero: not muted; MuteForever: no end
	EphemeralTimer  time.Duration // zero: disappearing messages off
	HistoryComplete bool          // the phone has no older messages for this chat
}

// GroupMeta is what the store knows about a group besides its title and
// members.
type GroupMeta struct {
	Topic            string
	Owner            IdentityID // 0 if unknown
	AnnounceOnly     bool
	Locked           bool
	ParticipantCount int       // from the last full member list
	FetchedAt        time.Time // when the full member list was stored; zero: never
}

// GroupMemberRow is a stored member of a group.
type GroupMemberRow struct {
	Identity IdentityID
	Role     GroupRole
}

// IsPinned reports whether the chat is pinned.
func (c *Chat) IsPinned() bool { return !c.PinnedAt.IsZero() }

// IsMuted reports whether the chat is muted at time now.
func (c *Chat) IsMuted(now time.Time) bool { return now.Before(c.MutedUntil) }

// ListBefore reports whether c sorts before d in the chat list: pinned
// chats first (most recently pinned first), then by latest activity,
// then by ID so the order is total and stable across restarts.
func (c *Chat) ListBefore(d *Chat) bool {
	if c.IsPinned() != d.IsPinned() {
		return c.IsPinned()
	}
	if !c.PinnedAt.Equal(d.PinnedAt) {
		return c.PinnedAt.After(d.PinnedAt)
	}
	if !c.LastActivity.Equal(d.LastActivity) {
		return c.LastActivity.After(d.LastActivity)
	}
	return c.ID > d.ID
}
