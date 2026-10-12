// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// Identity resolution (data-model.md §2, ADR-0009): the store links a
// person's phone-number JID and LID whenever an event names both. This
// file finds the second form where whatsmeow already knows it: in the
// event itself (SenderAlt, RecipientAlt, group participants) or in its
// LID map, a local table in session.db with an in-memory cache. It
// never asks the network.

// alt returns the other address form of a user JID from the LID map, or
// an empty JID. The container's map is used: a device that is not linked
// yet has no store of its own.
func (a *Adapter) alt(ctx context.Context, jid types.JID) types.JID {
	lids := a.sess.container.LIDMap
	if lids == nil {
		return types.EmptyJID
	}
	var other types.JID
	var err error
	switch jid.Server {
	case types.DefaultUserServer:
		other, err = lids.GetLIDForPN(ctx, jid.ToNonAD())
	case types.HiddenUserServer:
		other, err = lids.GetPNForLID(ctx, jid.ToNonAD())
	default:
		return types.EmptyJID
	}
	if err != nil {
		return types.EmptyJID
	}
	return other
}

// link returns an IdentityLinked event for two forms of one person, in
// either order, or nil.
func link(x, y types.JID) domain.Event {
	switch {
	case x.Server == types.DefaultUserServer && y.Server == types.HiddenUserServer:
		return domain.IdentityLinked{PN: fromJID(x), LID: fromJID(y)}
	case x.Server == types.HiddenUserServer && y.Server == types.DefaultUserServer:
		return domain.IdentityLinked{PN: fromJID(y), LID: fromJID(x)}
	}
	return nil
}

// resolveMessage adds what the event and the LID map know about the
// people of a message event: a link between the sender's two forms, and
// one for the peer of a direct chat. The links come first, so that the
// store merges before it files the message, and an edit or revoke finds
// its sender whichever form it uses.
func (a *Adapter) resolveMessage(ctx context.Context, info *types.MessageInfo, evs []domain.Event) []domain.Event {
	var links []domain.Event
	senderAlt := info.SenderAlt
	if senderAlt.IsEmpty() && !info.IsFromMe {
		senderAlt = a.alt(ctx, info.Sender)
	}
	if l := link(info.Sender, senderAlt); l != nil {
		links = append(links, l)
	}
	for i, ev := range evs {
		if m, ok := ev.(domain.MessageReceived); ok && m.Message.SenderAlt.IsZero() && !senderAlt.IsEmpty() {
			m.Message.SenderAlt = fromJID(senderAlt)
			evs[i] = m
		}
	}
	if !info.IsGroup && info.Chat.Server != types.BroadcastServer {
		peerAlt := info.RecipientAlt
		if !info.IsFromMe {
			peerAlt = senderAlt
		}
		if peerAlt.IsEmpty() {
			peerAlt = a.alt(ctx, info.Chat)
		}
		if l := link(info.Chat, peerAlt); l != nil && (len(links) == 0 || l != links[0]) {
			links = append(links, l)
		}
	}
	return append(links, evs...)
}

func role(admin, superAdmin bool) domain.GroupRole {
	switch {
	case superAdmin:
		return domain.RoleSuperAdmin
	case admin:
		return domain.RoleAdmin
	}
	return domain.RoleMember
}

// normalizeJoinedGroup describes a group this account joined (or was
// added to): its name and disappearing timer, and the full member list
// with both address forms of each participant.
func normalizeJoinedGroup(e *events.JoinedGroup) []domain.Event { return groupEvents(&e.GroupInfo) }

// groupEvents describes a group in full: title, disappearing timer,
// description and settings, and every member with both address forms.
func groupEvents(g *types.GroupInfo) []domain.Event {
	chat := fromJID(g.JID)
	timer := time.Duration(0)
	if g.IsEphemeral {
		timer = time.Duration(g.DisappearingTimer) * time.Second
	}
	meta := domain.GroupMetaUpdated{ChatJID: chat, Fields: domain.GroupFieldTopic | domain.GroupFieldAnnounce | domain.GroupFieldLocked,
		Topic: g.Topic, AnnounceOnly: g.IsAnnounce, Locked: g.IsLocked}
	if !g.OwnerJID.IsEmpty() {
		meta.Fields |= domain.GroupFieldOwner
		meta.Owner = fromJID(g.OwnerJID)
	}
	evs := []domain.Event{domain.ChatUpdated{ChatJID: chat, Fields: domain.ChatFieldTitle | domain.ChatFieldEphemeral,
		Title: g.Name, EphemeralTimer: timer}, meta}
	members := domain.GroupMembersChanged{ChatJID: chat, Full: true}
	for i := range g.Participants {
		p := &g.Participants[i]
		alt := p.LID
		if p.JID == p.LID {
			alt = p.PhoneNumber
		}
		members.Members = append(members.Members, domain.GroupMember{
			JID: fromJID(p.JID), AltJID: fromJID(alt), Role: role(p.IsAdmin, p.IsSuperAdmin),
		})
	}
	return append(evs, members)
}

// normalizeGroupInfo turns a change of a group into chat and member
// events. Joining members get their other form from the LID map.
func (a *Adapter) normalizeGroupInfo(ctx context.Context, e *events.GroupInfo) []domain.Event {
	chat := fromJID(e.JID)
	var evs []domain.Event
	if e.Name != nil {
		evs = append(evs, domain.ChatUpdated{ChatJID: chat, Fields: domain.ChatFieldTitle, Title: e.Name.Name})
	}
	if e.Ephemeral != nil {
		timer := time.Duration(0)
		if e.Ephemeral.IsEphemeral {
			timer = time.Duration(e.Ephemeral.DisappearingTimer) * time.Second
		}
		evs = append(evs, domain.ChatUpdated{ChatJID: chat, Fields: domain.ChatFieldEphemeral, EphemeralTimer: timer})
	}
	meta := domain.GroupMetaUpdated{ChatJID: chat}
	if e.Topic != nil {
		meta.Fields |= domain.GroupFieldTopic
		meta.Topic = e.Topic.Topic
		if e.Topic.TopicDeleted {
			meta.Topic = ""
		}
	}
	if e.Announce != nil {
		meta.Fields |= domain.GroupFieldAnnounce
		meta.AnnounceOnly = e.Announce.IsAnnounce
	}
	if e.Locked != nil {
		meta.Fields |= domain.GroupFieldLocked
		meta.Locked = e.Locked.IsLocked
	}
	if meta.Fields != 0 {
		evs = append(evs, meta)
	}
	members := domain.GroupMembersChanged{ChatJID: chat}
	add := func(jids []types.JID, r domain.GroupRole) {
		for _, j := range jids {
			members.Members = append(members.Members, domain.GroupMember{JID: fromJID(j), AltJID: fromJID(a.alt(ctx, j)), Role: r})
		}
	}
	add(e.Join, domain.RoleMember)
	add(e.Promote, domain.RoleAdmin)
	add(e.Demote, domain.RoleMember)
	for _, j := range e.Leave {
		members.Left = append(members.Left, fromJID(j))
	}
	if len(members.Members) > 0 || len(members.Left) > 0 {
		evs = append(evs, members)
	}
	return evs
}
