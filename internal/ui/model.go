// SPDX-License-Identifier: GPL-3.0-or-later

// Package ui is the terminal user interface: the Bubble Tea root model,
// its panes and the bridge that feeds it backend events. The root model
// is the single owner of UI state; it never performs I/O
// (docs/architecture/ui.md §1).
package ui

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/clipboard"
	"github.com/Yaman-yagiz/tarcin/internal/ui/composer"
	"github.com/Yaman-yagiz/tarcin/internal/ui/keymap"
	"github.com/Yaman-yagiz/tarcin/internal/ui/linkscreen"
	"github.com/Yaman-yagiz/tarcin/internal/ui/members"
	"github.com/Yaman-yagiz/tarcin/internal/ui/msglist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/search"
	"github.com/Yaman-yagiz/tarcin/internal/ui/theme"
	"github.com/Yaman-yagiz/tarcin/internal/ui/uikit"
)

// Options configure the root model.
type Options struct {
	Theme   *theme.Theme   // default: theme.Default()
	Keymap  *keymap.Keymap // default: keymap.Default()
	History msglist.History
	Names   msglist.Names
	// Send delivers a message the user wrote; it returns the command that
	// performs the send. Nil disables sending.
	Send func(out Outgoing) tea.Cmd
	// Read marks a chat as read when the user opens it; it returns the
	// command that does so. Nil disables it.
	Read func(chat domain.ChatID) tea.Cmd
	// Attention reports whether the user can see the open chat: false
	// when the terminal loses focus, true when it gets it back. While it
	// is false, the open chat's new messages stay unread. It returns the
	// command that does so. Nil disables it.
	Attention func(watching bool) tea.Cmd
	// Retry sends a failed own message again; it returns the command that
	// does so. Nil disables it.
	Retry func(msg domain.Message) tea.Cmd
	// Forward sends a copy of a message to another chat; it returns the
	// command that does so. Nil disables forwarding.
	Forward func(msg domain.Message, to domain.ChatID) tea.Cmd
	// DraftDir is the private directory for drafts handed to $EDITOR;
	// empty: $XDG_RUNTIME_DIR, or else the system's temporary directory.
	DraftDir string
	// Reconnect connects to WhatsApp again now, for :reconnect. Nil
	// disables it.
	Reconnect func() tea.Cmd
	// Link holds the actions of the link screen, shown while the device
	// is not linked to an account. Nil: the backend cannot link.
	Link *linkscreen.Options
	// Members reads groups for the members view. Nil disables it.
	Members members.Source
	// Search finds messages for :find and :findin. Nil disables them.
	Search search.Source
	// Edit replaces the text of this account's message, and Delete
	// deletes a message for everyone; each returns the command that does
	// so. Nil disables them.
	Edit   func(msg domain.Message, text string) tea.Cmd
	Delete func(msg domain.Message) tea.Cmd
	// React reacts to a message with an emoji, or removes this account's
	// reaction with an empty one; it returns the command that does so.
	// Nil disables it.
	React func(msg domain.Message, emoji string) tea.Cmd
	// Metrics returns the command that reports the app's counters for the
	// debug pane, as a MetricsMsg. Nil: the pane shows the UI's only.
	Metrics func() tea.Cmd
	// DeleteLocal deletes a chat and its messages from this computer only;
	// it returns the command that does so. Nil disables :delete-local.
	DeleteLocal func(chat domain.ChatID) tea.Cmd
	// ChatSetting changes a chat's settings, shared with the account's
	// other devices; it returns the command that does so. Nil disables
	// :mute, :pin, :archive and their opposites.
	ChatSetting func(chat domain.ChatID, s ChatSetting) tea.Cmd
	// Notify shows a desktop notification, or rings the bell; it returns
	// the command that does so. Nil: no notifications.
	Notify func(n Notification) tea.Cmd
	// NotifyFocused notifies while the terminal has focus too, for every
	// chat but the open one. False: only while it does not.
	NotifyFocused bool
	// StartChat finds or creates the direct chat with a contact, by name,
	// or with a phone number, for :new; the command reports a
	// ChatStartedMsg. Contacts lists the contacts whose names match, for
	// completing :new; the command reports a ContactsMsg. Nil disables
	// them.
	StartChat func(query string) tea.Cmd
	Contacts  func(query string) tea.Cmd
	// Attach queues a file to send to a chat, with out's text as its
	// caption; it returns the command that does so, which reports an
	// AttachResultMsg. Nil disables it.
	Attach func(out Outgoing, path string) tea.Cmd
	// OpenMedia opens a message's media and SaveMedia saves it, each
	// downloading it first if needed; they return the command that does
	// so. Nil disables them.
	OpenMedia func(msg domain.Message) tea.Cmd
	SaveMedia func(msg domain.Message) tea.Cmd
	// OpenLink opens a web address in the browser; it returns the
	// command that does so. Nil disables it.
	OpenLink func(url string) tea.Cmd
	// Typing tells a chat whether this account types in it, and Presence
	// tells contacts whether it is online; each returns the command that
	// does so. Nil, as the privacy settings may make them, sends nothing.
	Typing   func(chat domain.ChatID, composing bool) tea.Cmd
	Presence func(available bool) tea.Cmd
	// Mentionable returns the command that loads the people a mention in
	// a group can name, as a MentionCandidatesMsg. Nil disables mention
	// completion.
	Mentionable func(chat domain.ChatID) tea.Cmd
	// Older asks for messages older than the oldest stored one of a
	// chat, when it comes into view; it returns the command that does
	// so. Nil disables it.
	Older func(chat domain.ChatID, oldest domain.Message) tea.Cmd
	// Clock and Location override the wall clock and the local time zone,
	// for reproducible screens in tests.
	Clock    func() time.Time
	Location *time.Location
}

// Outgoing is a message the user wrote: its text, the message it answers
// if any, and the people it mentions, each as "@" and the user part of
// its JID in Text.
type Outgoing struct {
	Chat     domain.ChatID
	Text     string
	Quoted   domain.WAMessageID
	Mentions []domain.JID
}

// Focus identifies the pane that receives key input.
type Focus uint8

// Focusable panes, in Tab order.
const (
	FocusChatList Focus = iota
	FocusMessages
	FocusComposer
	focusCount
)

// String implements fmt.Stringer.
func (f Focus) String() string {
	return [...]string{"chat list", "messages", "composer"}[f]
}

// Model is the root model.
type Model struct {
	ctx    *uikit.Context
	keys   *keymap.Matcher
	layout Layout
	focus  Focus
	mode   mode

	chats                    *chatlist.Model
	msgs                     *msglist.Model
	comp                     *composer.Model
	list, messages, composer uikit.Pane
	send                     func(Outgoing) tea.Cmd
	names                    msglist.Names
	read                     func(domain.ChatID) tea.Cmd
	retry                    func(domain.Message) tea.Cmd
	forwardFn                func(domain.Message, domain.ChatID) tea.Cmd
	forwarding               *domain.Message // the message the chat search picks a chat for
	mediaHint                string          // the keys that open and save media, for the status line
	getenv                   func(string) string
	reconnect                func() tea.Cmd
	linkOpts                 *linkscreen.Options
	quitKeys                 []string
	members                  *members.Model // nil without Options.Members
	find                     *search.Model  // nil without Options.Search
	older                    func(domain.ChatID, domain.Message) tea.Cmd
	react                    func(domain.Message, string) tea.Cmd
	reactTo                  domain.Message // the message the reaction picker is for
	editFn                   func(domain.Message, string) tea.Cmd
	deleteFn                 func(domain.Message) tea.Cmd
	editing                  domain.Message                           // the message the composer edits
	presence                 map[domain.ChatID]domain.PresenceChanged // peers of direct chats
	now                      func() time.Time
	typingFn                 func(domain.ChatID, bool) tea.Cmd
	openMediaFn              func(domain.Message) tea.Cmd
	attachFn                 func(Outgoing, string) tea.Cmd
	notifyFn                 func(Notification) tea.Cmd
	settingFn                func(domain.ChatID, ChatSetting) tea.Cmd
	deleteLocalFn            func(domain.ChatID) tea.Cmd
	metricsFn                func() tea.Cmd
	metrics                  []MetricSection
	frames                   int
	confirmText              string
	confirmFn                func() tea.Cmd
	termFocus                int8
	attentionFn              func(bool) tea.Cmd
	notifyFocused            bool
	startChatFn              func(string) tea.Cmd
	choices                  string               // a completion's matches, shown beside the command line
	copyFn                   func(string) tea.Cmd // clipboard.Copy; a test seam
	openLinkFn               func(string) tea.Cmd
	linkHint                 string    // the key that opens a link, for the status line
	nextLink                 linkCycle // which of a message's links the key opens next
	hasLinks                 linkCache // whether the selected message has links
	contactsFn               func(string) tea.Cmd
	saveMediaFn              func(domain.Message) tea.Cmd
	presenceFn               func(bool) tea.Cmd
	available                bool
	lastKey                  time.Time
	composing                domain.ChatID
	typingSent               time.Time
	typingGen                int
	loc                      *time.Location
	mentionable              func(domain.ChatID) tea.Cmd
	mention                  mentions
	link                     *linkscreen.Model // the link screen, while linking

	header       string // current chat title
	conn         domain.ConnectionChanged
	notice       string // the latest notice; cleared by the next key
	showArchived bool

	input      textinput.Model // the command line and the search
	help       []string
	helpOffset int

	composerHeight int
	sep            string // pre-rendered vertical separator
	hsep           string // pre-rendered horizontal separator, sized to the chat
}

// New returns a root model. Panes that do not exist yet are placeholders.
func New(opts Options) *Model {
	t, km := opts.Theme, opts.Keymap
	if t == nil {
		t = theme.Default()
	}
	if km == nil {
		km = keymap.Default()
	}
	ctx := uikit.NewContext(t)
	var quitKeys []string // single keys that quit, for screens without the matcher
	quitHint, openKey, saveKey, linkKey := "", "", "", ""
	for _, b := range km.Bindings() {
		switch b.Action {
		case keymap.Quit:
			if len(b.Keys) == 1 {
				quitKeys = append(quitKeys, b.Keys[0])
			}
			if quitHint == "" {
				quitHint = b.String()
			}
		case keymap.OpenMedia:
			if openKey == "" {
				openKey = b.String()
			}
		case keymap.SaveMedia:
			if saveKey == "" {
				saveKey = b.String()
			}
		case keymap.OpenLink:
			if linkKey == "" {
				linkKey = b.String()
			}
		}
	}
	var mediaHint []string
	if openKey != "" {
		mediaHint = append(mediaHint, openKey+" open")
	}
	if saveKey != "" {
		mediaHint = append(mediaHint, saveKey+" save")
	}
	if opts.Link != nil {
		o := *opts.Link
		o.QuitKey = quitHint
		opts.Link = &o
	}
	chats := chatlist.New(ctx)
	msgs := msglist.New(ctx, opts.History, opts.Names)
	comp := composer.New(ctx)
	if opts.DraftDir != "" {
		comp.SetDraftDir(opts.DraftDir)
	}
	m := &Model{
		ctx:            ctx,
		keys:           keymap.NewMatcher(km),
		chats:          chats,
		msgs:           msgs,
		comp:           comp,
		send:           opts.Send,
		names:          opts.Names,
		read:           opts.Read,
		retry:          opts.Retry,
		forwardFn:      opts.Forward,
		mediaHint:      strings.Join(mediaHint, " · "),
		linkHint:       map[bool]string{true: linkKey + " open link"}[linkKey != ""],
		getenv:         os.Getenv,
		reconnect:      opts.Reconnect,
		linkOpts:       opts.Link,
		quitKeys:       quitKeys,
		older:          opts.Older,
		react:          opts.React,
		editFn:         opts.Edit,
		mentionable:    opts.Mentionable,
		presence:       map[domain.ChatID]domain.PresenceChanged{},
		typingFn:       opts.Typing,
		openMediaFn:    opts.OpenMedia,
		attachFn:       opts.Attach,
		notifyFn:       opts.Notify,
		attentionFn:    opts.Attention,
		copyFn:         clipboard.Copy,
		openLinkFn:     opts.OpenLink,
		notifyFocused:  opts.NotifyFocused,
		startChatFn:    opts.StartChat,
		contactsFn:     opts.Contacts,
		settingFn:      opts.ChatSetting,
		deleteLocalFn:  opts.DeleteLocal,
		metricsFn:      opts.Metrics,
		saveMediaFn:    opts.SaveMedia,
		presenceFn:     opts.Presence,
		now:            time.Now,
		loc:            time.Local,
		mention:        mentions{byChat: map[domain.ChatID][]candidate{}, loading: map[domain.ChatID]bool{}},
		deleteFn:       opts.Delete,
		list:           chats,
		messages:       msgs,
		composer:       comp,
		header:         "No chat selected",
		composerHeight: 1,
		input:          newInput(),
		help:           helpLines(km),
		conn:           domain.ConnectionChanged{State: domain.ConnConnecting},
	}
	if opts.Link != nil && opts.Link.Begin != nil {
		m.link = linkscreen.New(ctx, *opts.Link) // the first-run notice
	}
	if opts.Members != nil {
		m.members = members.New(ctx, opts.Members, opts.Names)
	}
	if opts.Search != nil {
		m.find = search.New(ctx, opts.Search, opts.Names, func(id domain.ChatID) string { t, _ := m.chats.Title(id); return t })
	}
	if opts.Clock != nil || opts.Location != nil {
		now, loc := opts.Clock, opts.Location
		if now == nil {
			now = time.Now
		}
		if loc == nil {
			loc = time.Local
		}
		m.now, m.loc = now, loc
		if m.find != nil {
			m.find.SetLocation(loc)
		}
		chats.SetClock(now, loc)
		msgs.SetClock(now, loc)
	}
	m.sep = t.Separator.Render("│")
	m.applyFocus()
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return nil }

// Layout returns the geometry of the last frame.
func (m *Model) Layout() Layout { return m.layout }

// Focus returns the focused pane.
func (m *Model) Focus() Focus { return m.focus }

// PendingKeys returns the keys of an incomplete key sequence, for the
// status line.
func (m *Model) PendingKeys() []string { return m.keys.Pending() }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case tea.ModeReportMsg:
		if msg.Mode == ansi.ModeUnicodeCore && supported(msg.Value) && m.ctx.Width != ansi.GraphemeWidth {
			// Mirror the renderer's switch to grapheme widths.
			m.ctx.Width = ansi.GraphemeWidth
			m.resize(m.layout.Width, m.layout.Height)
		}
		return m, nil
	case tea.KeyPressMsg:
		// A key press means the user is back, even if the terminal never
		// reported getting focus again. Unknown focus stays unknown, so
		// that terminals without focus reporting keep their notifications.
		var back tea.Cmd
		if m.termFocus == focusOut {
			back = m.setTermFocus(focusIn)
		}
		before := m.comp.Value()
		cmd := m.handleKey(msg)
		return m, tea.Batch(back, cmd, m.active(), m.typed(before))
	case tea.BlurMsg:
		cmd := m.setTermFocus(focusOut)
		return m, tea.Batch(cmd, m.goOffline())
	case tea.FocusMsg:
		cmd := m.setTermFocus(focusIn)
		return m, cmd
	case presenceCheckMsg:
		cmd := m.checkPresence()
		return m, cmd
	case typingIdleMsg:
		if msg.gen != m.typingGen {
			return m, nil
		}
		cmd := m.stopTyping()
		return m, cmd
	case BatchMsg:
		cmd := m.applyBatch(msg)
		return m, cmd
	case chatlist.LoadMsg, chatlist.UpdateMsg:
		return m, m.chats.Update(msg)
	case chatlist.OpenMsg:
		cmd := m.open(msg.ID)
		return m, cmd
	case msglist.PageMsg, msglist.AppendMsg, msglist.ReplaceMsg, msglist.ReloadMsg, msglist.NamesChangedMsg:
		return m, m.msgs.Update(msg)
	case linkscreen.CodeMsg, linkscreen.ResultMsg:
		if m.link == nil {
			return m, nil
		}
		return m, m.link.Update(msg)
	case AttachResultMsg:
		m.attached(msg)
		return m, nil
	case pathCompletionMsg:
		m.completed(msg)
		return m, nil
	case ContactsMsg:
		m.completedContacts(msg)
		return m, nil
	case clipboard.ResultMsg:
		m.copied(msg)
		return m, nil
	case ChatStartedMsg:
		cmd := m.chatStarted(msg)
		return m, cmd
	case MetricsMsg:
		m.metrics = msg.Sections
		return m, nil
	case debugTickMsg:
		if m.mode != modeDebug {
			return m, nil
		}
		cmd := m.refreshDebug()
		return m, cmd
	case MentionCandidatesMsg:
		cmd := m.setCandidates(msg)
		return m, cmd
	case msglist.OldestMsg:
		if m.older == nil || msg.Chat != m.msgs.Chat() {
			return m, nil
		}
		cmd := m.older(msg.Chat, msg.Oldest)
		return m, cmd
	case search.ResultsMsg:
		if m.find == nil {
			return m, nil
		}
		return m, m.find.Update(msg)
	case search.OpenMsg:
		cmd := m.openResult(msg)
		return m, cmd
	case members.InfoMsg, members.PageMsg:
		if m.members == nil {
			return m, nil
		}
		return m, m.members.Update(msg)
	case tea.PasteMsg:
		if m.link != nil {
			return m, m.link.Update(msg)
		}
		if m.focus != FocusComposer {
			return m, nil
		}
		cmd := m.composer.Update(msg)
		m.fitComposer()
		return m, cmd
	case composer.SendMsg:
		if m.send == nil {
			return m, nil
		}
		cmd := tea.Batch(m.stopTyping(), m.send(Outgoing{Chat: msg.Chat, Text: msg.Text, Quoted: msg.QuotedWAID, Mentions: msg.Mentions}))
		return m, cmd
	case composer.EditMsg:
		if m.editFn == nil || msg.WAID != m.editing.WAID {
			return m, nil
		}
		cmd := m.editFn(m.editing, msg.Text)
		m.fitComposer()
		return m, cmd
	case composer.ErrorMsg:
		m.notice = msg.Err.Error()
		return m, nil
	case msglist.NoticeMsg:
		m.notice = msg.Text
		return m, nil
	case pagerDoneMsg:
		if msg.err != nil {
			m.notice = "The pager failed: " + msg.err.Error() + " (set $PAGER)"
		}
		return m, nil
	case msglist.MessageActionMsg:
		if msg.Action == keymap.Reply {
			m.startReply(&msg.Message)
			return m, nil
		}
		switch msg.Action {
		case keymap.React:
			m.startReaction(&msg.Message)
			return m, nil
		case keymap.Edit:
			m.startEdit(&msg.Message)
			return m, nil
		case keymap.Delete:
			m.startDelete(&msg.Message)
			return m, nil
		case keymap.Forward:
			m.startForward(&msg.Message)
			return m, nil
		case keymap.Copy:
			cmd := m.copyMessage(&msg.Message)
			return m, cmd
		case keymap.ViewPager:
			cmd := m.pageMessage(&msg.Message)
			return m, cmd
		case keymap.OpenMedia, keymap.SaveMedia:
			cmd := m.mediaAction(msg.Action, &msg.Message)
			return m, cmd
		case keymap.OpenLink:
			cmd := m.openLink(&msg.Message)
			return m, cmd
		}
		if msg.Action == keymap.Retry && m.retry != nil {
			if !msg.Message.FromMe || msg.Message.Status != domain.StatusFailed {
				m.notice = "Only a message that failed to send can be sent again"
				return m, nil
			}
			cmd := m.retry(msg.Message)
			return m, cmd
		}
		m.notice = string(msg.Action) + " is not available yet"
		return m, nil
	default:
		// Internal messages of the composer (editor round trip).
		cmd := m.comp.Update(msg)
		m.fitComposer()
		return m, cmd
	}
}

// applyBatch applies a bridge batch: chat rows and typing go to the chat
// list; the open chat's messages and reloads go to the message view, and
// changes of the group shown to the members view.
func (m *Model) applyBatch(b BatchMsg) tea.Cmd {
	cmd := tea.Batch(m.applyRows(b), m.notifications(b))
	if m.members != nil && m.members.Chat() != 0 {
		switch shown := m.members.Chat(); {
		case slices.Contains(b.Removed, shown):
			m.closeMembers()
		case slices.Contains(b.Groups, shown):
			cmd = tea.Batch(cmd, m.members.Reload())
		}
	}
	return cmd
}

func (m *Model) applyRows(b BatchMsg) tea.Cmd {
	if len(b.Rows) > 0 {
		m.chats.Update(chatlist.UpdateMsg{Rows: b.Rows})
	}
	for chat, typing := range b.Typing {
		m.chats.SetTyping(chat, typing)
	}
	for chat, p := range b.Presence {
		m.presence[chat] = p
	}
	if b.Connection != nil {
		m.conn = *b.Connection
	}
	if b.Notice != "" {
		m.notice = b.Notice
	}
	for _, id := range b.Removed {
		m.chats.Remove(id)
		if id == m.msgs.Chat() {
			m.closeChat()
			m.notice = "This chat was merged into another chat with the same person"
		}
	}
	if b.NamesChanged {
		m.msgs.Update(msglist.NamesChangedMsg{})
	}
	if b.Link != nil && m.linkOpts != nil && !b.Linked {
		if m.link == nil {
			o := *m.linkOpts
			o.Begin = nil // the notice was shown at start
			m.link = linkscreen.New(m.ctx, o)
			m.link.Resize(m.layout.Width, max(m.layout.Height-1, 0))
		}
		m.link.Progress(*b.Link)
	}
	if b.Linked && m.link != nil {
		m.link = nil
		m.notice = "Linked. Your chats are coming over from the phone"
	}
	open := m.msgs.Chat()
	if open == 0 {
		return nil
	}
	var cmds []tea.Cmd
	if slices.Contains(b.Reloads, open) {
		return m.msgs.Update(msglist.ReloadMsg{Chat: open})
	}
	if msgs := b.Appends[open]; len(msgs) > 0 {
		cmds = append(cmds, m.msgs.Update(msglist.AppendMsg{Chat: open, Msgs: msgs}))
	}
	if msgs := b.Replaces[open]; len(msgs) > 0 {
		cmds = append(cmds, m.msgs.Update(msglist.ReplaceMsg{Chat: open, Msgs: msgs}))
	}
	return tea.Batch(cmds...)
}

// fitComposer resizes the layout when the composer's height changed.
func (m *Model) fitComposer() {
	if h := m.comp.Height(); h != m.composerHeight {
		m.composerHeight = h
		m.resize(m.layout.Width, m.layout.Height)
	}
}

// open shows a chat: its title in the header, its newest messages and
// the focus on them.
func (m *Model) open(id domain.ChatID) tea.Cmd {
	title, ok := m.chats.Title(id)
	if !ok {
		return nil
	}
	m.header = title
	cmd := m.msgs.Open(id)
	m.comp.SetChat(id)
	m.fitComposer()
	m.setFocus(FocusMessages)
	if m.read != nil {
		cmd = tea.Batch(cmd, m.read(id))
	}
	return cmd
}

// mediaAction opens or saves a message's media.
func (m *Model) mediaAction(a keymap.Action, msg *domain.Message) tea.Cmd {
	fn, verb := m.openMediaFn, "Opening"
	if a == keymap.SaveMedia {
		fn, verb = m.saveMediaFn, "Saving"
	}
	switch {
	case fn == nil:
		m.notice = "media is not available"
		return nil
	case !msg.Kind.HasMedia() || msg.IsRevoked():
		m.notice = "This message has no media"
		return nil
	case msg.Flags.Has(domain.FlagViewOnce):
		m.notice = "View-once media opens only on your phone"
		return nil
	}
	m.notice = verb + "…"
	return fn(*msg)
}

// startEdit puts this account's message in the composer for editing.
func (m *Model) startEdit(msg *domain.Message) {
	switch {
	case m.editFn == nil:
		m.notice = "editing is not available"
	case !msg.FromMe || msg.Kind != domain.KindText || msg.IsRevoked() || msg.WAID == "":
		m.notice = "Only your own text messages can be edited"
	case time.Since(msg.Timestamp) > domain.EditWindow:
		m.notice = "A message can be edited for 20 minutes after it is sent"
	default:
		m.editing = *msg
		m.comp.SetEdit(msg.WAID, msg.Body)
		m.setFocus(FocusComposer)
		m.fitComposer()
	}
}

// startDelete asks, in the status line, whether to delete a message for
// everyone.
func (m *Model) startDelete(msg *domain.Message) {
	switch {
	case m.deleteFn == nil:
		m.notice = "deleting is not available"
	case msg.IsRevoked() || msg.WAID == "" || msg.Kind == domain.KindSystem:
		m.notice = "This message cannot be deleted"
	default:
		target := *msg
		prompt := "Delete this message for everyone? y/n"
		if !msg.FromMe {
			prompt = "Delete this message for everyone, as a group admin? y/n"
		}
		m.ask(prompt, func() tea.Cmd { return m.deleteFn(target) })
	}
}

// ask puts a yes/no question in the status line; y runs fn.
func (m *Model) ask(prompt string, fn func() tea.Cmd) {
	m.confirmText, m.confirmFn = prompt, fn
	m.keys.Reset()
	m.mode = modeConfirm
}

// deleteLocalCommand runs :delete-local on the chat selected in the
// list, or the open one, after asking.
func (m *Model) deleteLocalCommand() tea.Cmd {
	chat := m.msgs.Chat()
	if m.focus == FocusChatList || chat == 0 {
		chat, _ = m.chats.Selected()
	}
	switch {
	case m.deleteLocalFn == nil:
		m.notice = "deleting chats is not available"
		return nil
	case chat == 0:
		m.notice = "select a chat first"
		return nil
	}
	title, _ := m.chats.Title(chat)
	m.ask("Delete "+oneLine(title)+" and its messages from this computer? Your phone keeps them. y/n",
		func() tea.Cmd { return m.deleteLocalFn(chat) })
	return nil
}

// handleConfirmKey answers the deletion question: y deletes, anything
// else keeps the message.
func (m *Model) handleConfirmKey(msg tea.KeyPressMsg) tea.Cmd {
	m.mode = modeNormal
	fn := m.confirmFn
	m.confirmFn = nil
	if k := msg.String(); (k == "y" || k == "Y") && fn != nil {
		cmd := fn()
		return cmd
	}
	m.notice = "nothing changed"
	return nil
}

// confirmPrompt is the status line of a yes/no question.
func (m *Model) confirmPrompt() string { return m.confirmText }

// quickReactions are the reactions the picker offers on 1 to 6, as
// WhatsApp's: thumbs up, red heart, tears of joy, open mouth, crying,
// folded hands.
var quickReactions = []string{"\U0001F44D", "\u2764\ufe0f", "\U0001F602", "\U0001F62E", "\U0001F622", "\U0001F64F"}

// startReaction opens the reaction picker for msg in the status line.
func (m *Model) startReaction(msg *domain.Message) {
	if !m.canReact(msg) {
		return
	}
	m.reactTo = *msg
	m.keys.Reset()
	m.mode = modeReact
}

func (m *Model) canReact(msg *domain.Message) bool {
	switch {
	case m.react == nil:
		m.notice = "reactions are not available"
	case msg.WAID == "" || msg.IsRevoked() || msg.Kind == domain.KindSystem || (msg.FromMe && msg.Status == domain.StatusFailed):
		m.notice = "This message cannot get a reaction"
	default:
		return true
	}
	return false
}

// handleReactKey picks a reaction: 1 to 6 react, 0 or backspace removes
// this account's reaction, anything else cancels.
func (m *Model) handleReactKey(msg tea.KeyPressMsg) tea.Cmd {
	m.mode = modeNormal
	key := msg.String()
	switch {
	case len(key) == 1 && key[0] >= '1' && int(key[0]-'1') < len(quickReactions):
		return m.react(m.reactTo, quickReactions[key[0]-'1'])
	case key == "0" || key == "backspace":
		return m.react(m.reactTo, "")
	}
	return nil
}

// reactCommand runs ":react [EMOJI]" on the selected message: with an
// emoji it reacts with it, without one it opens the picker.
func (m *Model) reactCommand(emoji string) tea.Cmd {
	msg, ok := m.msgs.SelectedMessage()
	if !ok {
		m.notice = "select a message to react to"
		return nil
	}
	if emoji == "" {
		m.startReaction(&msg)
		return nil
	}
	if !m.canReact(&msg) {
		return nil
	}
	if len([]rune(emoji)) > 16 {
		m.notice = "a reaction is one emoji"
		return nil
	}
	return m.react(msg, emoji)
}

// reactPrompt is the status line of the reaction picker.
func reactPrompt() string {
	var b strings.Builder
	b.WriteString("react:")
	for i, e := range quickReactions {
		b.WriteString(" " + strconv.Itoa(i+1) + " " + e)
	}
	b.WriteString(" · 0 remove · esc cancel")
	return b.String()
}

// startReply makes the composer answer msg and focuses it.
func (m *Model) startReply(msg *domain.Message) {
	switch {
	case m.send == nil:
		m.notice = "sending is not available"
		return
	case msg.WAID == "" || msg.IsRevoked() || msg.Kind == domain.KindSystem:
		m.notice = "This message cannot be answered"
		return
	}
	name := "You"
	if !msg.FromMe && m.names != nil {
		name = m.names.DisplayName(msg.SenderID)
	}
	text := msg.Body
	if text == "" {
		text = msg.Kind.String()
	}
	if r := []rune(text); len(r) > 120 {
		text = string(r[:120])
	}
	m.comp.SetReply(msg.WAID, name+": "+text)
	m.setFocus(FocusComposer)
	m.fitComposer()
}

// findCommand runs ":find QUERY" (all chats) and ":findin QUERY" (the
// open chat).
func (m *Model) findCommand(query string, here bool) tea.Cmd {
	chat := domain.ChatID(0)
	switch {
	case m.find == nil:
		m.notice = "message search is not available"
		return nil
	case query == "":
		m.notice = "usage: :find WORDS, or :findin WORDS in the open chat"
		return nil
	case here:
		if chat = m.msgs.Chat(); chat == 0 {
			m.notice = "open a chat to search in it"
			return nil
		}
	}
	m.keys.Reset()
	m.mode = modeFind
	cmd := m.find.Open(query, chat)
	return cmd
}

// handleFindKey drives the search results: navigation keys move, Enter
// or l opens a result, esc or q closes them.
func (m *Model) handleFindKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if slices.Contains(m.quitKeys, key) {
		return tea.Quit
	}
	for _, c := range []keymap.Context{keymap.ChatList, keymap.Navigation} {
		res := m.keys.Feed(c, key)
		switch {
		case res.Pending:
			return nil
		case res.Handled():
			cmd := m.find.Update(keymap.ActionMsg{Action: res.Action})
			return cmd
		}
	}
	if key == "esc" || key == "q" {
		m.mode = modeNormal
	}
	return nil
}

// openResult opens a search result's chat at its message.
func (m *Model) openResult(r search.OpenMsg) tea.Cmd {
	m.mode = modeNormal
	title, ok := m.chats.Title(r.Chat)
	if !ok {
		m.notice = "that chat is no longer here"
		return nil
	}
	m.chats.Select(r.Chat)
	m.header = title
	cmd := m.msgs.OpenAt(r.Chat, r.WAID)
	m.comp.SetChat(r.Chat)
	m.fitComposer()
	m.setFocus(FocusMessages)
	if m.read != nil {
		cmd = tea.Batch(cmd, m.read(r.Chat))
	}
	return cmd
}

// openMembers shows the members of the open group.
func (m *Model) openMembers() tea.Cmd {
	chat := m.msgs.Chat()
	switch {
	case m.members == nil:
		m.notice = "the members view is not available"
		return nil
	case chat == 0 || !m.chats.IsGroup(chat):
		m.notice = "open a group to see its members"
		return nil
	}
	title, _ := m.chats.Title(chat)
	m.keys.Reset()
	m.mode = modeMembers
	return m.members.Open(chat, title)
}

func (m *Model) closeMembers() {
	if m.mode == modeMembers {
		m.mode = modeNormal
	}
}

// handleMembersKey drives the members view: navigation keys move, quit
// quits, and esc or q closes it.
func (m *Model) handleMembersKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if slices.Contains(m.quitKeys, key) {
		return tea.Quit
	}
	res := m.keys.Feed(keymap.Navigation, key)
	switch {
	case res.Pending:
		return nil
	case res.Handled():
		return m.members.Update(keymap.ActionMsg{Action: res.Action})
	}
	switch key {
	case "esc", "q", "enter":
		m.closeMembers()
	}
	return nil
}

// closeChat shows no chat, keeping the composer's drafts.
func (m *Model) closeChat() {
	m.msgs.Close()
	m.header = "No chat selected"
	m.comp.SetChat(0)
	m.fitComposer()
	m.setFocus(FocusChatList)
}

// focusContext maps the focused pane to its keymap context.
func (m *Model) focusContext() keymap.Context {
	switch m.focus {
	case FocusMessages:
		return keymap.Messages
	case FocusComposer:
		return keymap.Compose
	}
	return keymap.ChatList
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	if m.link != nil {
		// The link screen takes every key but quit.
		res := m.keys.Feed(keymap.Global, msg.String())
		switch {
		case res.Pending:
			return nil
		case res.Handled() && res.Action == keymap.Quit:
			return tea.Quit
		}
		return m.link.Update(msg)
	}
	switch m.mode {
	case modeCommand, modeSearch:
		return m.handleInputKey(msg)
	case modeHelp:
		return m.handleHelpKey(msg)
	case modeMembers:
		return m.handleMembersKey(msg)
	case modeFind:
		return m.handleFindKey(msg)
	case modeReact:
		return m.handleReactKey(msg)
	case modeConfirm:
		return m.handleConfirmKey(msg)
	case modeDebug:
		return m.debugKey(msg)
	}
	if m.handleSuggestionKey(msg.String()) {
		return nil
	}
	res := m.keys.Feed(m.focusContext(), msg.String())
	switch {
	case res.Pending:
		return nil
	case !res.Handled():
		// Unbound keys go to the focused pane, e.g. text for the composer.
		cmd := m.focused().Update(msg)
		m.fitComposer()
		if m.focus == FocusComposer {
			cmd = tea.Batch(cmd, m.updateMentions())
		}
		return cmd
	}
	switch res.Action {
	case keymap.Quit:
		return tea.Quit
	case keymap.FocusNext:
		m.setFocus((m.focus + 1) % focusCount)
	case keymap.FocusPrev:
		m.setFocus((m.focus + focusCount - 1) % focusCount)
	case keymap.ComposeMsg:
		m.setFocus(FocusComposer)
	case keymap.NormalMode:
		if m.comp.ClearReply() || m.comp.CancelEdit() {
			m.fitComposer() // the first Esc drops the reply or the edit
			return nil
		}
		m.setFocus(FocusMessages)
	case keymap.Help:
		m.mode, m.helpOffset = modeHelp, 0
	case keymap.Command:
		m.startInput(modeCommand)
	case keymap.Search:
		m.startInput(modeSearch)
	case keymap.NextUnread:
		return m.nextUnread(1)
	case keymap.PrevUnread:
		return m.nextUnread(-1)
	default:
		cmd := m.focused().Update(keymap.ActionMsg{Action: res.Action})
		m.fitComposer()
		return cmd
	}
	return nil
}

func supported(v ansi.ModeSetting) bool {
	return v == ansi.ModeSet || v == ansi.ModeReset || v == ansi.ModePermanentlySet
}

func (m *Model) focused() uikit.Pane {
	switch m.focus {
	case FocusMessages:
		return m.messages
	case FocusComposer:
		return m.composer
	}
	return m.list
}

func (m *Model) setFocus(f Focus) {
	m.keys.Reset()
	m.focus = f
	m.applyFocus()
	if m.layout.Narrow {
		// In narrow mode the focus decides which screen is shown.
		m.resize(m.layout.Width, m.layout.Height)
	}
}

func (m *Model) applyFocus() {
	m.list.SetFocused(m.focus == FocusChatList)
	m.messages.SetFocused(m.focus == FocusMessages)
	m.composer.SetFocused(m.focus == FocusComposer)
}

func (m *Model) resize(w, h int) {
	m.layout = computeLayout(w, h, m.composerHeight, m.focus == FocusChatList)
	l := m.layout
	m.list.Resize(l.List.W, l.List.H)
	m.messages.Resize(l.Messages.W, l.Messages.H)
	m.composer.Resize(l.Composer.W, l.Composer.H)
	m.hsep = m.ctx.Theme.Separator.Render(strings.Repeat("─", max(l.Messages.W, 0)))
	if m.link != nil {
		m.link.Resize(w, max(h-1, 0))
	}
	if m.members != nil {
		m.members.Resize(w, max(h-1, 0))
	}
	if m.find != nil {
		m.find.Resize(w, max(h-1, 0))
	}
}

// View implements tea.Model. It costs O(visible cells): every pane renders
// only what fits, and rows are assembled from the pane lines.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.ReportFocus = true
	v.WindowTitle = "tarcin"
	l := m.layout
	switch {
	case l.TooSmall || l.Height == 0:
	case m.mode == modeCommand || m.mode == modeSearch:
		if c := m.input.Cursor(); c != nil {
			c.X += m.ctx.Width.StringWidth(" "+m.modeName()+" ") + 1
			c.Y = l.Status.Y
			v.Cursor = c
		}
	case m.mode == modeNormal && m.focus == FocusComposer && l.ShowChat:
		if c := m.comp.Cursor(); c != nil && c.X < l.Composer.W && c.Y < l.Composer.H {
			c.X += l.Composer.X
			c.Y += l.Composer.Y
			v.Cursor = c
		}
	}
	return v
}

func (m *Model) render() string {
	m.frames++
	l := m.layout
	if l.Width == 0 || l.Height == 0 {
		return ""
	}
	if l.TooSmall {
		return m.tooSmall()
	}
	method := m.ctx.Width
	body := l.Height - 1
	if m.link != nil {
		lines := uikit.FitLines(method, strings.Join(m.link.View(), "\n"), l.Width, body)
		return strings.Join(append(lines, m.statusLine(l.Width)), "\n")
	}
	if m.mode == modeHelp {
		lines := uikit.FitLines(method, m.helpView(body), l.Width, body)
		return strings.Join(append(lines, m.statusLine(l.Width)), "\n")
	}
	if m.mode == modeMembers {
		lines := uikit.FitLines(method, m.members.View(), l.Width, body)
		return strings.Join(append(lines, m.statusLine(l.Width)), "\n")
	}
	if m.mode == modeFind {
		lines := uikit.FitLines(method, m.find.View(), l.Width, body)
		return strings.Join(append(lines, m.statusLine(l.Width)), "\n")
	}
	if m.mode == modeDebug {
		lines := uikit.FitLines(method, m.debugView(), l.Width, body)
		return strings.Join(append(lines, m.statusLine(l.Width)), "\n")
	}

	var list, chat []string
	if l.ShowList {
		list = uikit.FitLines(method, m.list.View(), l.List.W, body)
	}
	if l.ShowChat {
		chat = m.chatLines(body)
	}

	var b strings.Builder
	b.Grow(l.Width * l.Height * 2)
	for y := range body {
		switch {
		case l.ShowList && l.ShowChat:
			b.WriteString(list[y])
			b.WriteString(m.sep)
			b.WriteString(chat[y])
		case l.ShowList:
			b.WriteString(list[y])
		default:
			b.WriteString(chat[y])
		}
		b.WriteByte('\n')
	}
	b.WriteString(m.statusLine(l.Width))
	return b.String()
}

// chatLines renders the chat column: header, messages, separator and
// composer, as body lines of the chat width.
func (m *Model) chatLines(body int) []string {
	l, method := m.layout, m.ctx.Width
	header := m.ctx.Theme.Header
	if m.focus != FocusChatList {
		header = m.ctx.Theme.HeaderFocused
	}
	lines := make([]string, 0, body)
	title := header.Render(uikit.Display(method, m.header))
	if s := m.chatStatus(); s != "" {
		title += m.ctx.Theme.Muted.Render(" · " + s)
	}
	lines = append(lines, uikit.Fit(method, title, l.Header.W))
	lines = append(lines, uikit.FitLines(method, m.messages.View(), l.Messages.W, l.Messages.H)...)
	lines = append(lines, m.hsep)
	lines = append(lines, uikit.FitLines(method, m.composer.View(), l.Composer.W, l.Composer.H)...)
	return lines
}

// chatStatus says what the open chat's people are doing: typing, or for a
// direct chat whether the peer is online or when it was last seen.
func (m *Model) chatStatus() string {
	open := m.msgs.Chat()
	if open == 0 {
		return ""
	}
	if m.chats.IsTyping(open) {
		return "typing…"
	}
	if d := m.chats.Disappearing(open); d > 0 {
		return "messages disappear after " + timerLabel(d)
	}
	p, ok := m.presence[open]
	switch {
	case !ok:
		return ""
	case p.Online:
		return "online"
	case !p.LastSeen.IsZero():
		return "last seen " + lastSeen(p.LastSeen, m.now(), m.loc)
	}
	return ""
}

// timerLabel names a disappearing-message timer: "24 hours", "7 days".
func timerLabel(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/time.Minute), "minute")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// lastSeen describes a time relative to now: a time today, "yesterday
// 21:04", or a date.
func lastSeen(t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	y, mo, d := now.Date()
	today := time.Date(y, mo, d, 0, 0, 0, 0, loc)
	switch {
	case !t.Before(today):
		return "today " + t.Format("15:04")
	case !t.Before(today.AddDate(0, 0, -1)):
		return "yesterday " + t.Format("15:04")
	}
	return t.Format("2 Jan 15:04")
}

func (m *Model) tooSmall() string {
	l := m.layout
	msg := "Terminal too small"
	lines := uikit.FitLines(m.ctx.Width, "", l.Width, l.Height)
	lines[l.Height/2] = uikit.Fit(m.ctx.Width, strings.Repeat(" ", max((l.Width-len(msg))/2, 0))+msg, l.Width)
	return strings.Join(lines, "\n")
}
