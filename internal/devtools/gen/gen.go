// SPDX-License-Identifier: GPL-3.0-or-later

// Package gen builds deterministic synthetic datasets for benchmarks, load
// tests and the demo mode: the scenarios S1–S4 and S12 of
// docs/architecture/performance.md. A dataset describes identities and
// chats in memory and streams messages chat by chat, so even the
// million-message stress scenario never has to fit in memory.
package gen

import (
	"fmt"
	"iter"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/devtools/synth"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// DefaultEnd is the timestamp of the newest generated message. It is
// fixed, never the wall clock, so datasets are identical on every run.
var DefaultEnd = time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)

// Spec describes a dataset.
type Spec struct {
	Name     string
	Seed     uint64
	Chats    int
	Messages int // total across all chats
	// HotChatMessages, if set, puts exactly this many of Messages into
	// chat 0; the rest are spread over the other chats with a skew.
	HotChatMessages int
	GroupRatio      float64 // share of chats that are groups
	MembersPerGroup int
	HotGroupMembers int     // if set, chat 0 is a group with this many members
	StressNames     bool    // long, emoji, RTL and invisible-character names
	HostileRate     float64 // share of bodies taken from synth.Hostile
	SameSecondRate  float64 // share of messages sharing the previous message's second
	Span            time.Duration
	End             time.Time
}

// Scenarios are the named datasets from the performance plan.
var Scenarios = map[string]Spec{
	"S1":  {Name: "S1", Seed: 1, Chats: 50, Messages: 10_000, GroupRatio: 0.4, MembersPerGroup: 40},
	"S2":  {Name: "S2", Seed: 2, Chats: 200, Messages: 100_000, HotChatMessages: 10_000, GroupRatio: 0.4, MembersPerGroup: 60},
	"S3":  {Name: "S3", Seed: 3, Chats: 500, Messages: 1_000_000, HotChatMessages: 100_000, GroupRatio: 0.4, MembersPerGroup: 80},
	"S4":  {Name: "S4", Seed: 4, Chats: 20, Messages: 20_000, HotChatMessages: 10_000, HotGroupMembers: 5_000, GroupRatio: 0.5, MembersPerGroup: 50, StressNames: true},
	"S12": {Name: "S12", Seed: 12, Chats: 10, Messages: 2_000, HotChatMessages: 1_000, GroupRatio: 0.5, MembersPerGroup: 20, StressNames: true, HostileRate: 0.2},
}

// Dataset is a generated account. Identities and chats are held in
// memory; messages are produced on demand by Messages and All.
type Dataset struct {
	Spec       Spec
	Me         domain.Identity
	Identities []domain.Identity // including Me, indexed by ID-1
	Chats      []domain.Chat     // index i has ID i+1
	Members    map[domain.ChatID][]domain.IdentityID

	counts  []int                 // messages per chat
	firstID []domain.MessageID    // first message ID of each chat
	peers   [][]domain.IdentityID // possible senders per chat, excluding me
	seeds   []uint64              // per-chat message stream seeds
}

// Build generates the dataset described by spec. It panics if the spec is
// inconsistent, since specs are static test inputs.
func Build(spec Spec) *Dataset {
	spec = withDefaults(spec)
	if spec.HotChatMessages > spec.Messages || spec.Chats < 1 {
		panic(fmt.Sprintf("gen: inconsistent spec %+v", spec))
	}
	g := synth.New(spec.Seed)
	d := &Dataset{Spec: spec, Members: map[domain.ChatID][]domain.IdentityID{}}

	newIdentity := func(name string, me bool) domain.Identity {
		id := domain.Identity{ID: domain.IdentityID(len(d.Identities) + 1), LID: g.LID(), PN: g.PN(), PushName: name, IsMe: me}
		d.Identities = append(d.Identities, id)
		return id
	}
	name := g.Name
	if spec.StressNames {
		name = g.StressName
	}
	d.Me = newIdentity("Me", true)

	d.counts = distribute(spec)
	groups := int(math.Round(float64(spec.Chats) * spec.GroupRatio))
	var next domain.MessageID = 1
	for i := range spec.Chats {
		chat := domain.Chat{ID: domain.ChatID(i + 1)}
		var peers []domain.IdentityID
		switch {
		case i == 0 && spec.HotGroupMembers > 0, i < groups:
			members := spec.MembersPerGroup
			if i == 0 && spec.HotGroupMembers > 0 {
				members = spec.HotGroupMembers
			}
			chat.Kind, chat.JID, chat.Title = domain.ChatGroup, g.GroupJID(), g.GroupTitle()
			ids := []domain.IdentityID{d.Me.ID}
			for range members {
				p := newIdentity(name(), false)
				ids = append(ids, p.ID)
				peers = append(peers, p.ID)
			}
			d.Members[chat.ID] = ids
		default:
			p := newIdentity(name(), false)
			chat.Kind, chat.JID, chat.PeerID = domain.ChatDirect, p.LID, p.ID
			peers = []domain.IdentityID{p.ID}
		}
		d.peers = append(d.peers, peers)
		d.seeds = append(d.seeds, g.Rand().Uint64())
		d.firstID = append(d.firstID, next)
		next += domain.MessageID(d.counts[i])
		d.Chats = append(d.Chats, chat)
	}
	d.decorate(rand.New(rand.NewPCG(spec.Seed, 0xc4a7))) //nolint:gosec // synthetic data
	return d
}

func withDefaults(spec Spec) Spec {
	if spec.MembersPerGroup <= 0 {
		spec.MembersPerGroup = 30
	}
	if spec.SameSecondRate == 0 {
		spec.SameSecondRate = 0.1
	}
	if spec.Span <= 0 {
		spec.Span = 365 * 24 * time.Hour
	}
	if spec.End.IsZero() {
		spec.End = DefaultEnd
	}
	return spec
}

// distribute splits spec.Messages over the chats: chat 0 gets
// HotChatMessages if set, and the rest follow a Zipf-like skew, using the
// largest-remainder method so the counts add up exactly.
func distribute(spec Spec) []int {
	counts := make([]int, spec.Chats)
	rest, from := spec.Messages, 0
	if spec.HotChatMessages > 0 {
		counts[0] = spec.HotChatMessages
		rest -= spec.HotChatMessages
		from = 1
	}
	n := spec.Chats - from
	if n == 0 {
		counts[0] += rest
		return counts
	}
	weights := make([]float64, n)
	var sum float64
	for i := range weights {
		weights[i] = 1 / math.Pow(float64(i+1), 0.8)
		sum += weights[i]
	}
	type rem struct {
		i    int
		frac float64
	}
	rems := make([]rem, n)
	assigned := 0
	for i, w := range weights {
		exact := float64(rest) * w / sum
		counts[from+i] = int(exact)
		assigned += int(exact)
		rems[i] = rem{i: from + i, frac: exact - math.Floor(exact)}
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for k := 0; assigned < rest; k++ {
		counts[rems[k%n].i]++
		assigned++
	}
	return counts
}

// decorate sets list metadata: last activity, unread counts, pins,
// archives and mutes.
func (d *Dataset) decorate(r *rand.Rand) {
	for i := range d.Chats {
		c := &d.Chats[i]
		if d.counts[i] > 0 {
			c.LastMessageID = d.firstID[i] + domain.MessageID(d.counts[i]) - 1
			c.LastActivity = d.chatEnd(i)
		}
		c.LastReadAt = c.LastActivity
		if r.IntN(4) == 0 && d.counts[i] > 0 {
			c.UnreadCount = 1 + r.IntN(min(d.counts[i], 50))
			c.LastReadAt = c.LastActivity.Add(-time.Hour)
		}
		switch {
		case i < 2:
			c.PinnedAt = d.Spec.End.Add(-time.Duration(i+1) * time.Hour)
		case i%17 == 3:
			c.Archived = true
		case i%11 == 5:
			c.MutedUntil = domain.MuteForever
		}
	}
}

// chatEnd returns the newest message time of chat i. Chats end at
// staggered times so the chat list has a stable, varied order.
func (d *Dataset) chatEnd(i int) time.Time {
	return d.Spec.End.Add(-time.Duration(i) * 7 * time.Minute)
}

// MessageCount returns the number of messages in chat index i.
func (d *Dataset) MessageCount(i int) int { return d.counts[i] }

// TotalMessages returns the number of messages in the dataset.
func (d *Dataset) TotalMessages() int { return d.Spec.Messages }

// Messages streams the messages of chat index i, oldest first, with IDs
// increasing in the same order. The stream is identical on every call.
func (d *Dataset) Messages(i int) iter.Seq[domain.Message] {
	return func(yield func(domain.Message) bool) {
		n := d.counts[i]
		if n == 0 {
			return
		}
		spec := d.Spec
		g := synth.New(d.seeds[i])
		r := g.Rand()
		var hostile []string
		if spec.HostileRate > 0 {
			hostile = synth.Hostile()
		}
		chat := d.Chats[i]
		end := d.chatEnd(i)
		gap := spec.Span / time.Duration(n)
		ts := end.Add(-gap * time.Duration(n)).Truncate(time.Second)
		var recent []domain.WAMessageID
		for k := range n {
			if k > 0 && r.Float64() >= spec.SameSecondRate {
				ts = ts.Add(time.Second + time.Duration(r.Int64N(int64(2*gap))))
				ts = ts.Truncate(time.Second)
			}
			if k == n-1 {
				ts = end.Truncate(time.Second)
			}
			m := domain.Message{
				ID:        d.firstID[i] + domain.MessageID(k),
				ChatID:    chat.ID,
				WAID:      g.WAID(),
				Timestamp: ts,
				Kind:      domain.KindText,
				Body:      g.Message(),
				Status:    domain.StatusRead,
			}
			if r.IntN(100) < 15 {
				m.FromMe, m.SenderID = true, d.Me.ID
				if r.IntN(50) == 0 {
					m.Status = domain.StatusFailed
				}
			} else {
				peers := d.peers[i]
				m.SenderID = peers[r.IntN(len(peers))]
				m.Status = domain.StatusDelivered
			}
			if spec.HostileRate > 0 && r.Float64() < spec.HostileRate {
				m.Body = hostile[r.IntN(len(hostile))]
			}
			if len(recent) > 0 && r.IntN(10) == 0 {
				m.QuotedWAID = recent[r.IntN(len(recent))]
			}
			if r.IntN(25) == 0 {
				m.Kind = domain.KindImage
				m.Media = &domain.Media{MIME: "image/jpeg", Size: 50_000 + r.Int64N(2_000_000), Width: 1600, Height: 1200}
			}
			if r.IntN(200) == 0 {
				m.EditedAt = ts.Add(time.Minute)
			}
			if r.IntN(300) == 0 {
				m.RevokedAt, m.Body = ts.Add(time.Minute), ""
			}
			recent = append(recent, m.WAID)
			if len(recent) > 30 {
				recent = recent[1:]
			}
			if !yield(m) {
				return
			}
		}
	}
}

// All streams every message, chat by chat.
func (d *Dataset) All() iter.Seq[domain.Message] {
	return func(yield func(domain.Message) bool) {
		for i := range d.Chats {
			for m := range d.Messages(i) {
				if !yield(m) {
					return
				}
			}
		}
	}
}
