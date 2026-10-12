// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"slices"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// MaxChangedMessages is how many changed messages per chat one Changes
// value carries. Beyond it the chat is marked for a reload instead, which
// keeps notifications small during a history sync. It matches the UI
// bridge's per-chat buffer.
const MaxChangedMessages = 200

// Changes describes what one commit changed, for UI notifications. It is
// a hint (guarantee G6 in docs/architecture/event-pipeline.md): the
// database stays the source of truth.
//
// Consumers read the fields; the writer's Applier fills them through the
// methods, which keep each message's latest version only and enforce the
// per-chat cap.
type Changes struct {
	// Added and Updated hold new and changed messages per chat, in the
	// order they were written.
	Added   map[domain.ChatID][]domain.Message
	Updated map[domain.ChatID][]domain.Message
	// Reload lists chats with too many changes to list: their visible
	// window must be reloaded from the database.
	Reload map[domain.ChatID]struct{}
	// Chats lists chats whose chat-list entry changed.
	Chats map[domain.ChatID]struct{}
	// Identities lists people whose names changed or who were merged
	// away.
	Identities map[domain.IdentityID]struct{}
	// Removed lists chats that no longer exist, such as a person's second
	// direct chat after their identities were merged.
	Removed map[domain.ChatID]struct{}
	// Groups lists groups whose members or metadata changed.
	Groups map[domain.ChatID]struct{}
	// Erased reports that content was deleted for good: a chat deleted or
	// cleared, or messages expired. The writer then empties the WAL,
	// which still holds the deleted rows (security.md §3).
	Erased bool
	// Unlinked lists the media cache files that no stored message uses
	// after the commit, for the caller to delete.
	Unlinked []string

	where map[domain.MessageID]messageRef
}

// messageRef locates a message in Added or Updated.
type messageRef struct {
	chat  domain.ChatID
	added bool
	i     int
}

func (c *Changes) init() {
	if c.where != nil {
		return
	}
	c.Added = map[domain.ChatID][]domain.Message{}
	c.Updated = map[domain.ChatID][]domain.Message{}
	c.Reload = map[domain.ChatID]struct{}{}
	c.Chats = map[domain.ChatID]struct{}{}
	c.Identities = map[domain.IdentityID]struct{}{}
	c.Removed = map[domain.ChatID]struct{}{}
	c.Groups = map[domain.ChatID]struct{}{}
	c.where = map[domain.MessageID]messageRef{}
}

// AddMessage records a newly inserted message.
func (c *Changes) AddMessage(m *domain.Message) { c.put(m, true) }

// UpdateMessage records a message whose stored form changed.
func (c *Changes) UpdateMessage(m *domain.Message) { c.put(m, false) }

func (c *Changes) put(m *domain.Message, added bool) {
	c.init()
	if _, ok := c.Reload[m.ChatID]; ok {
		return
	}
	if _, ok := c.Removed[m.ChatID]; ok {
		return
	}
	if ref, ok := c.where[m.ID]; ok {
		if ref.added {
			c.Added[ref.chat][ref.i] = *m
		} else {
			c.Updated[ref.chat][ref.i] = *m
		}
		return
	}
	if len(c.Added[m.ChatID])+len(c.Updated[m.ChatID]) >= MaxChangedMessages {
		c.ReloadChat(m.ChatID)
		return
	}
	if added {
		c.where[m.ID] = messageRef{chat: m.ChatID, added: true, i: len(c.Added[m.ChatID])}
		c.Added[m.ChatID] = append(c.Added[m.ChatID], *m)
	} else {
		c.where[m.ID] = messageRef{chat: m.ChatID, i: len(c.Updated[m.ChatID])}
		c.Updated[m.ChatID] = append(c.Updated[m.ChatID], *m)
	}
}

// ReloadChat replaces the chat's listed messages with a reload marker.
func (c *Changes) ReloadChat(id domain.ChatID) {
	c.init()
	if _, ok := c.Removed[id]; ok {
		return
	}
	c.dropMessages(id)
	c.Reload[id] = struct{}{}
}

// RemoveChat records that a chat no longer exists. Chat IDs are never
// reused, so nothing more is recorded for it.
func (c *Changes) RemoveChat(id domain.ChatID) {
	c.init()
	c.dropMessages(id)
	delete(c.Reload, id)
	delete(c.Chats, id)
	delete(c.Groups, id)
	c.Removed[id] = struct{}{}
}

func (c *Changes) dropMessages(id domain.ChatID) {
	for i := range c.Added[id] {
		delete(c.where, c.Added[id][i].ID)
	}
	for i := range c.Updated[id] {
		delete(c.where, c.Updated[id][i].ID)
	}
	delete(c.Added, id)
	delete(c.Updated, id)
}

// TouchChat records that a chat's chat-list entry changed.
func (c *Changes) TouchChat(id domain.ChatID) {
	c.init()
	if _, ok := c.Removed[id]; !ok {
		c.Chats[id] = struct{}{}
	}
}

// TouchGroup records that a group's members or metadata changed.
func (c *Changes) TouchGroup(id domain.ChatID) {
	c.init()
	if _, ok := c.Removed[id]; !ok {
		c.Groups[id] = struct{}{}
	}
}

// TouchIdentity records that a person's names changed.
func (c *Changes) TouchIdentity(id domain.IdentityID) {
	c.init()
	c.Identities[id] = struct{}{}
}

// Empty reports whether nothing was recorded.
func (c *Changes) Empty() bool {
	return len(c.Added) == 0 && len(c.Updated) == 0 && len(c.Reload) == 0 &&
		len(c.Chats) == 0 && len(c.Identities) == 0 && len(c.Removed) == 0 && len(c.Groups) == 0 &&
		len(c.Unlinked) == 0
}

// Unlink records a cache file that no stored message uses any more.
func (c *Changes) Unlink(name string) {
	if !slices.Contains(c.Unlinked, name) {
		c.Unlinked = append(c.Unlinked, name)
	}
}

// Merge adds everything recorded in o, as if it had been recorded after
// what c holds.
func (c *Changes) Merge(o *Changes) {
	for _, msgs := range o.Added {
		for i := range msgs {
			c.AddMessage(&msgs[i])
		}
	}
	for _, msgs := range o.Updated {
		for i := range msgs {
			c.UpdateMessage(&msgs[i])
		}
	}
	c.Erased = c.Erased || o.Erased
	for _, name := range o.Unlinked {
		c.Unlink(name)
	}
	for id := range o.Reload {
		c.ReloadChat(id)
	}
	for id := range o.Chats {
		c.TouchChat(id)
	}
	for id := range o.Identities {
		c.TouchIdentity(id)
	}
	for id := range o.Removed {
		c.RemoveChat(id)
	}
	for id := range o.Groups {
		c.TouchGroup(id)
	}
}

// reset empties c, keeping its maps for reuse.
func (c *Changes) reset() {
	clear(c.Added)
	clear(c.Updated)
	clear(c.Reload)
	clear(c.Chats)
	clear(c.Identities)
	clear(c.Removed)
	clear(c.Groups)
	clear(c.where)
	c.Erased = false
	c.Unlinked = c.Unlinked[:0]
}
