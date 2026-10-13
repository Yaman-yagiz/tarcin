// SPDX-License-Identifier: GPL-3.0-or-later

package msglist

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/sanitize"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

const (
	// maxBodyLines caps a message's height in the list; longer messages
	// end with a hint to open them in the pager.
	maxBodyLines = 40
	// groupGap is how long consecutive messages from one sender share a
	// header.
	groupGap = 5 * time.Minute
	// gutter is the width of the selection bar left of every line.
	gutter = 1
	indent = "  "
)

// cacheKey identifies one rendered message. Everything that changes its
// lines is part of the key, so an entry never needs invalidation.
type cacheKey struct {
	id       domain.MessageID
	rev      uint32
	width    int
	method   ansi.Method
	theme    string
	namesRev uint32
	header   bool   // the sender header is shown (not grouped)
	day      string // day separator label, "" for none
	// quoteID and quoteRev identify the quoted message: zero for no quote,
	// -1 when the quoted message is not stored, -2 and -3 for the store's
	// preview of it when it is not in the window (-3: deleted).
	quoteID  domain.MessageID
	quoteRev uint32
}

// height returns the number of lines message i renders to.
func (m *Model) height(i int) int { return len(m.lines(i)) }

// lines returns the rendered lines of message i, from the cache when
// possible. Lines are at most the content width wide (without gutter).
func (m *Model) lines(i int) []string {
	msg := &m.win[i]
	key := cacheKey{
		id: msg.ID, rev: msg.Revision, width: m.w, method: m.ctx.Width,
		theme: m.ctx.Theme.ID, namesRev: m.namesRev,
	}
	key.header, key.day = m.contextOf(i)
	key.quoteID, key.quoteRev = m.quoteKey(msg)
	m.lookups++
	if l, ok := m.cache.Get(key); ok {
		return l
	}
	m.misses++
	l := m.render(msg, key)
	m.cache.Put(key, l)
	return l
}

// contextOf decides whether message i starts a new sender group and
// whether a day separator precedes it. The first window message gets both
// unless older messages are known to be absent.
func (m *Model) contextOf(i int) (header bool, day string) {
	msg := &m.win[i]
	if i == 0 {
		if m.olderDone {
			return true, m.dayLabel(msg.Timestamp)
		}
		return true, ""
	}
	prev := &m.win[i-1]
	if !sameDay(prev.Timestamp, msg.Timestamp, m.loc) {
		return true, m.dayLabel(msg.Timestamp)
	}
	grouped := prev.SenderID == msg.SenderID && prev.FromMe == msg.FromMe &&
		msg.Timestamp.Sub(prev.Timestamp) < groupGap &&
		prev.Kind != domain.KindSystem && msg.Kind != domain.KindSystem
	return !grouped, ""
}

// quoteKey identifies the quoted message for the cache key without
// building the snippet, which happens only on a cache miss.
func (m *Model) quoteKey(msg *domain.Message) (domain.MessageID, uint32) {
	if msg.QuotedWAID == "" {
		return 0, 0
	}
	if i, ok := m.byWAID[msg.QuotedWAID]; ok {
		return m.win[i].ID, m.win[i].Revision
	}
	if q := msg.Quote; q != nil {
		// The store's preview: it differs from none, and a deletion shows.
		if q.Revoked {
			return -3, 0
		}
		return -2, 0
	}
	return -1, 0
}

func (m *Model) quoteOf(msg *domain.Message) string {
	if msg.QuotedWAID == "" {
		return ""
	}
	// The window's copy is the freshest; else the preview the store read
	// with the reply.
	var q domain.Quote
	if i, ok := m.byWAID[msg.QuotedWAID]; ok {
		w := &m.win[i]
		q = domain.Quote{SenderID: w.SenderID, FromMe: w.FromMe, Kind: w.Kind, Body: w.Body, Revoked: w.IsRevoked()}
	} else if msg.Quote != nil {
		q = *msg.Quote
	} else {
		return "↩ quoted message"
	}
	text := strings.Join(strings.Fields(sanitize.Line(q.Body)), " ")
	if q.Revoked {
		text = "deleted message"
	} else if text == "" {
		text = q.Kind.String()
	}
	if r := []rune(text); len(r) > 80 { // the line is truncated anyway; keep keys small
		text = string(r[:80])
	}
	return "↩ " + m.senderName(&domain.Message{SenderID: q.SenderID, FromMe: q.FromMe}) + ": " + text
}

func (m *Model) senderName(msg *domain.Message) string {
	if msg.FromMe {
		return "You"
	}
	name := ""
	if m.names != nil {
		name = sanitize.Line(m.names.DisplayName(msg.SenderID))
	}
	if name == "" {
		name = "unknown"
	}
	return name
}

func (m *Model) dayLabel(t time.Time) string {
	now := m.now().In(m.loc)
	t = t.In(m.loc)
	switch {
	case sameDay(t, now, m.loc):
		return "Today"
	case sameDay(t, now.AddDate(0, 0, -1), m.loc):
		return "Yesterday"
	}
	return t.Format("Monday, 2 January 2006")
}

func sameDay(a, b time.Time, loc *time.Location) bool {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	return ay == by && am == bm && ad == bd
}

// render lays out one message: an optional day separator, an optional
// header (sender and time), an optional quote, and the wrapped body.
func (m *Model) render(msg *domain.Message, key cacheKey) []string {
	t := m.ctx.Theme
	method := m.ctx.Width
	w := m.w - gutter
	if w <= 0 {
		return []string{""}
	}
	var out []string

	if key.day != "" {
		label := " " + key.day + " "
		side := max((w-method.StringWidth(label))/2, 0)
		line := strings.Repeat("─", side) + label + strings.Repeat("─", side)
		out = append(out, t.Day.Render(uikit.Truncate(method, line, w, "")))
	}

	if msg.Kind == domain.KindSystem {
		text := uikit.Display(method, sanitize.Line(msg.Body))
		line := "— " + text + " —"
		pad := max((w-method.StringWidth(line))/2, 0)
		return append(out, t.Muted.Render(uikit.Truncate(method, strings.Repeat(" ", pad)+line, w, "…")))
	}

	if key.header {
		name := m.ctx.Theme.Sender(int64(msg.SenderID)).Render(uikit.Display(method, m.senderName(msg)))
		if msg.FromMe {
			name = t.Accent.Bold(true).Render("You")
		}
		meta := msg.Timestamp.In(m.loc).Format("15:04")
		if !msg.EditedAt.IsZero() && !msg.IsRevoked() {
			meta += " (edited)"
		}
		if msg.Flags.Has(domain.FlagEphemeral) {
			meta += " ⏱" // disappears
		}
		line := name + "  " + t.Muted.Render(meta)
		if msg.Flags.Has(domain.FlagMentionsMe) && !msg.IsRevoked() {
			line += "  " + t.Accent.Bold(true).Render("@ mentions you")
		}
		out = append(out, uikit.Truncate(method, line, w, "…"))
	}

	if key.quoteID != 0 {
		out = append(out, t.Quote.Render(uikit.Truncate(method, indent+uikit.Display(method, m.quoteOf(msg)), w, "…")))
	}

	bodyW := w - len(indent)
	if bodyW <= 0 {
		return append(out, "")
	}
	for _, l := range m.bodyLines(msg, bodyW) {
		out = append(out, indent+l)
	}
	if line := m.reactionLine(msg); line != "" && !msg.IsRevoked() {
		out = append(out, uikit.Truncate(method, indent+line, w, "…"))
	}
	return out
}

// withMentions shows each mention in text, "@" and the user part of a
// JID, as "@" and the person's current name, highlighted; a mention of
// this account most. A person whose name is not known keeps the token.
func (m *Model) withMentions(msg *domain.Message, text string) string {
	if len(msg.Mentions) == 0 {
		return text
	}
	t := m.ctx.Theme
	for _, mn := range msg.Mentions {
		if mn.Identity == 0 || !isMentionToken(mn.Token) || !strings.Contains(text, mn.Token) {
			continue
		}
		name := strings.Join(strings.Fields(sanitize.Line(m.names.DisplayName(mn.Identity))), " ")
		if name == "" {
			continue
		}
		style := t.Accent
		if name == "You" {
			style = t.Accent.Bold(true)
		}
		text = replaceToken(text, mn.Token, style.Render("@"+uikit.Display(m.ctx.Width, name)))
	}
	return text
}

// replaceToken replaces whole-word occurrences of token: "@90555" must not
// match inside "@905551".
func replaceToken(text, token, with string) string {
	var b strings.Builder
	for {
		i := strings.Index(text, token)
		if i < 0 {
			b.WriteString(text)
			return b.String()
		}
		end := i + len(token)
		b.WriteString(text[:i])
		if end < len(text) && isDigit(text[end]) {
			b.WriteString(token)
		} else {
			b.WriteString(with)
		}
		text = text[end:]
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isMentionToken reports whether a mention's token has the form the
// adapter writes, "@" and a JID's user part. The token comes from the
// network: an ASCII one can never match inside a multibyte character.
func isMentionToken(tok string) bool {
	if len(tok) < 2 || tok[0] != '@' {
		return false
	}
	for i := 1; i < len(tok); i++ {
		c := tok[i]
		if !isDigit(c) && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// reactionLine shows a message's reactions, "❤ 2  👍 1", most frequent
// first; this account's own is highlighted. Emoji come from the network
// and are sanitized; a "reaction" that is no single emoji is cut short.
func (m *Model) reactionLine(msg *domain.Message) string {
	if len(msg.Reactions) == 0 {
		return ""
	}
	t := m.ctx.Theme
	parts := make([]string, 0, len(msg.Reactions))
	for _, r := range msg.Reactions {
		emoji := sanitize.Line(r.Emoji)
		if g := []rune(emoji); len(g) > 8 {
			emoji = string(g[:8])
		}
		part := uikit.Display(m.ctx.Width, emoji) + " " + strconv.Itoa(r.Count)
		if r.Mine {
			parts = append(parts, t.Accent.Render(part))
		} else {
			parts = append(parts, t.Muted.Render(part))
		}
	}
	return strings.Join(parts, "  ")
}

// bodyLines wraps the body (or its placeholder) to width, capped at
// maxBodyLines, and appends delivery ticks to own messages.
func (m *Model) bodyLines(msg *domain.Message, width int) []string {
	t := m.ctx.Theme
	method := m.ctx.Width

	var text string
	switch {
	case msg.IsRevoked():
		text = t.Muted.Italic(true).Render("This message was deleted")
	default:
		text = m.withMentions(msg, uikit.Display(method, sanitize.Text(msg.Body)))
		if p := uikit.Display(method, placeholder(msg)); p != "" {
			p = t.Muted.Render(p)
			if text == "" {
				text = p
			} else {
				text = p + "\n" + text
			}
		}
		if msg.Flags.Has(domain.FlagForwarded) {
			text = t.Muted.Render("↪ forwarded") + "\n" + text
		}
	}
	if msg.FromMe {
		text += " " + ticks(t, msg.Status)
	}

	var lines []string
	for i, para := range strings.Split(text, "\n") {
		if para == "" {
			lines = append(lines, "")
		} else {
			id := "m" + strconv.FormatInt(int64(msg.ID), 10) + "p" + strconv.Itoa(i)
			lines = append(lines, linkify(para, wrap(method, para, width), id)...)
		}
		if len(lines) > maxBodyLines {
			break
		}
	}
	if len(lines) > maxBodyLines {
		lines = append(lines[:maxBodyLines-1], t.Muted.Render(uikit.Truncate(method, "… long message: press v to read it all", width, "…")))
	}
	return lines
}

// wrap wraps a paragraph to width cells. Wrap can leave a line wider
// than that, such as leading spaces before a word that fills it; such a
// line is wrapped hard, and what still does not fit (a wide character in
// a column too narrow for it) is cut.
func wrap(method ansi.Method, para string, width int) []string {
	var out []string
	for _, l := range strings.Split(method.Wrap(para, width, ""), "\n") {
		if method.StringWidth(l) <= width {
			out = append(out, l)
			continue
		}
		for _, h := range strings.Split(method.Hardwrap(l, width, true), "\n") {
			out = append(out, uikit.Truncate(method, h, width, ""))
		}
	}
	return out
}

// placeholder describes non-text content.
func placeholder(msg *domain.Message) string {
	if msg.Flags.Has(domain.FlagViewOnce) && msg.Kind.HasMedia() {
		// WhatsApp shows view-once media only on the phone.
		return "[view once " + msg.Kind.String() + " · open it on your phone]"
	}
	size := ""
	if msg.Media != nil && msg.Media.Size > 0 {
		size = " · " + humanSize(msg.Media.Size)
	}
	if msg.Media != nil {
		switch msg.Media.State {
		case domain.MediaDownloading:
			size += " · downloading…"
		case domain.MediaFailed:
			size += " · download failed"
		case domain.MediaExpired:
			size += " · no longer available"
		}
	}
	dur := ""
	if msg.Media != nil && msg.Media.Duration > 0 {
		d := msg.Media.Duration.Round(time.Second)
		dur = fmt.Sprintf(" · %d:%02d", int(d.Minutes()), int(d.Seconds())%60)
	}
	switch msg.Kind {
	case domain.KindImage:
		return "[image" + size + "]"
	case domain.KindVideo:
		return "[video" + dur + size + "]"
	case domain.KindAudio:
		return "[voice message" + dur + "]"
	case domain.KindDocument:
		name := ""
		if msg.Media != nil && msg.Media.FileName != "" {
			name = " · " + sanitize.Line(msg.Media.FileName)
		}
		return "[document" + name + size + "]"
	case domain.KindSticker:
		return "[sticker]"
	case domain.KindLocation:
		return "[location]"
	case domain.KindContact:
		return "[contact card]"
	case domain.KindPoll:
		return "[poll]"
	case domain.KindUnsupported:
		return "[unsupported message]"
	}
	return ""
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

// ticks shows the delivery state of an own message. Every state has its
// own symbol, so color is never the only signal (ui.md §9).
func ticks(t *theme.Theme, s domain.MessageStatus) string {
	switch s {
	case domain.StatusPending:
		return t.Muted.Render("◷")
	case domain.StatusSent:
		return t.Muted.Render("✓")
	case domain.StatusDelivered:
		return t.Muted.Render("✓✓")
	case domain.StatusRead, domain.StatusPlayed:
		return t.Accent.Render("✔✔")
	case domain.StatusFailed:
		return t.Warning.Render("✗ not sent")
	}
	return ""
}

// View implements uikit.Pane. It walks up from the anchor and joins the
// cached lines of the visible messages only.
func (m *Model) View() string {
	if m.w <= 0 || m.h <= 0 {
		return ""
	}
	t := m.ctx.Theme
	switch {
	case m.chat == 0:
		return m.centered(t.Muted.Render("Select a chat to read it"))
	case m.loading:
		return m.centered(t.Muted.Render("Loading…"))
	case m.err != nil && len(m.win) == 0:
		return m.centered(t.Warning.Render("Could not load messages: " + sanitize.Line(m.err.Error())))
	case len(m.win) == 0:
		return m.centered(t.Muted.Render("No messages yet"))
	}

	type chunk struct {
		lines    []string
		selected bool
		lead     int // leading day-separator lines, which get no selection bar
	}
	chunkOf := func(i int, lines []string) chunk {
		c := chunk{lines: lines, selected: m.win[i].ID == m.selected}
		if _, day := m.contextOf(i); day != "" {
			c.lead = 1
		}
		return c
	}
	chunks := make([]chunk, 0, 16)
	i := m.idx[m.anchor]
	ls := m.lines(i)
	ls = ls[:len(ls)-m.skip]
	need := m.h - len(ls)
	chunks = append(chunks, chunkOf(i, ls))
	for need > 0 && i > 0 {
		i--
		ls = m.lines(i)
		chunks = append(chunks, chunkOf(i, ls))
		need -= len(ls)
	}

	rows := make([]string, 0, m.h)
	for range need { // fewer lines than the viewport: sit at the bottom
		rows = append(rows, "")
	}
	bar, blank := t.Gutter.Render("\u258c"), " "
	for c := len(chunks) - 1; c >= 0; c-- {
		ch := chunks[c]
		lines := ch.lines
		if c == len(chunks)-1 && need < 0 {
			lines = lines[-need:] // the top message is partly above the viewport
			ch.lead = max(ch.lead+need, 0)
		}
		for k, l := range lines {
			g := blank
			if ch.selected && m.focused && k >= ch.lead {
				g = bar
			}
			rows = append(rows, g+l)
		}
	}
	if !m.follow && m.unseen > 0 && len(rows) > 0 {
		plural := "s"
		if m.unseen == 1 {
			plural = ""
		}
		rows[len(rows)-1] = blank + t.Indicator.Render(fmt.Sprintf(" ↓ %d new message%s (G) ", m.unseen, plural))
	}
	return strings.Join(rows, "\n")
}

func (m *Model) centered(s string) string {
	return strings.Repeat("\n", m.h/2) + "  " + s
}
