// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/textfold"
)

// maxSuggestions is how many names the mention line offers.
const maxSuggestions = 5

// MentionCandidate is a person a mention in a chat can name.
type MentionCandidate struct {
	Name string
	JID  domain.JID
}

// MentionCandidatesMsg delivers the people a mention in a chat can name,
// from Options.Mentionable.
type MentionCandidatesMsg struct {
	Chat       domain.ChatID
	Candidates []MentionCandidate
}

// candidate is a MentionCandidate with its search key.
type candidate struct {
	MentionCandidate
	folded string
}

// mentions is the state of mention completion in the composer.
type mentions struct {
	byChat    map[domain.ChatID][]candidate
	loading   map[domain.ChatID]bool
	shown     []candidate
	pick      int
	dismissed string // a query the user closed the suggestions for
}

// updateMentions offers the names a mention being typed in a group can
// complete to, loading the group's people the first time.
func (m *Model) updateMentions() tea.Cmd {
	q, typing := m.comp.MentionQuery()
	chat := m.msgs.Chat()
	if !typing || m.mentionable == nil || !m.chats.IsGroup(chat) || q == m.mention.dismissed {
		if !typing {
			m.mention.dismissed = ""
		}
		m.showSuggestions(nil)
		return nil
	}
	cands, ok := m.mention.byChat[chat]
	if !ok {
		if m.mention.loading[chat] {
			return nil
		}
		m.mention.loading[chat] = true
		cmd := m.mentionable(chat)
		return cmd
	}
	m.showSuggestions(matchMentions(cands, q))
	return nil
}

// matchMentions ranks the candidates for a query: names that start with
// it, then names with a word that does, then names that contain it.
func matchMentions(cands []candidate, query string) []candidate {
	q := textfold.Fold(query)
	type ranked struct {
		c    candidate
		rank int
	}
	var hits []ranked
	for _, c := range cands {
		rank := -1
		switch {
		case strings.HasPrefix(c.folded, q):
			rank = 0
		case strings.Contains(c.folded, " "+q):
			rank = 1
		case strings.Contains(c.folded, q):
			rank = 2
		}
		if rank >= 0 {
			hits = append(hits, ranked{c, rank})
		}
	}
	slices.SortStableFunc(hits, func(a, b ranked) int { return a.rank - b.rank })
	out := make([]candidate, 0, min(len(hits), maxSuggestions))
	for _, h := range hits[:min(len(hits), maxSuggestions)] {
		out = append(out, h.c)
	}
	return out
}

func (m *Model) showSuggestions(shown []candidate) {
	m.mention.shown = shown
	m.mention.pick = min(m.mention.pick, max(len(shown)-1, 0))
	names := make([]string, len(shown))
	for i, c := range shown {
		names[i] = c.Name
	}
	if len(names) == 0 {
		names = nil
	}
	m.comp.SetSuggestions(names, m.mention.pick)
	m.fitComposer()
}

// handleSuggestionKey drives the suggestions while they show: Tab
// completes, ctrl+n and ctrl+p move, Esc closes them. It reports whether
// it took the key.
func (m *Model) handleSuggestionKey(key string) bool {
	if m.focus != FocusComposer || len(m.mention.shown) == 0 {
		return false
	}
	switch key {
	case "tab":
		c := m.mention.shown[m.mention.pick]
		m.comp.CompleteMention(c.Name, c.JID)
		m.mention.pick = 0
		m.showSuggestions(nil)
	case "ctrl+n":
		m.mention.pick = (m.mention.pick + 1) % len(m.mention.shown)
		m.showSuggestions(m.mention.shown)
	case "ctrl+p":
		m.mention.pick = (m.mention.pick + len(m.mention.shown) - 1) % len(m.mention.shown)
		m.showSuggestions(m.mention.shown)
	case "esc":
		m.mention.dismissed, _ = m.comp.MentionQuery()
		m.showSuggestions(nil)
	default:
		return false
	}
	return true
}

// setCandidates stores a chat's people, sorted by name.
func (m *Model) setCandidates(msg MentionCandidatesMsg) tea.Cmd {
	delete(m.mention.loading, msg.Chat)
	cands := make([]candidate, 0, len(msg.Candidates))
	for _, c := range msg.Candidates {
		if c.Name == "" || c.JID.IsZero() {
			continue
		}
		cands = append(cands, candidate{MentionCandidate: c, folded: textfold.Fold(c.Name)})
	}
	slices.SortFunc(cands, func(a, b candidate) int { return strings.Compare(a.folded, b.folded) })
	m.mention.byChat[msg.Chat] = cands
	return m.updateMentions()
}
