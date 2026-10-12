// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/textfold"
	"github.com/Yaman-yagiz/tarcin/internal/ui"
	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
	"github.com/Yaman-yagiz/tarcin/internal/ui/composer"
)

// Starting a chat (":new"): with a contact of the address book, by name,
// or with a phone number, which the server is asked about once. The
// direct chat is found, or created empty, and opened.

// contactChoices is how many matching contacts a completion or an
// ambiguous name lists.
const contactChoices = 8

// startChat is the ui.Options.StartChat function.
func (p *pipeline) startChat(query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		row, err := p.chatWith(ctx, query)
		if err != nil {
			return composer.ErrorMsg{Err: err}
		}
		return ui.ChatStartedMsg{Row: row}
	}
}

// contacts is the ui.Options.Contacts function: the names of the
// contacts that match query, for completing ":new".
func (p *pipeline) contacts(query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		found, err := p.reader.Contacts(ctx, query, contactChoices+1)
		if err != nil {
			return composer.ErrorMsg{Err: err}
		}
		names := make([]string, len(found))
		for i := range found {
			names[i] = found[i].ContactName
		}
		return ui.ContactsMsg{Query: query, Names: names}
	}
}

// chatWith finds or creates the direct chat with the person query names,
// and returns its chat-list row.
func (p *pipeline) chatWith(ctx context.Context, query string) (chatlist.Row, error) {
	jid, evs, err := p.addressOf(ctx, query)
	if err != nil {
		return chatlist.Row{}, err
	}
	id, ok, err := p.reader.ChatID(ctx, jid)
	if err != nil {
		return chatlist.Row{}, err
	}
	if !ok {
		evs = append(evs, domain.ChatUpdated{ChatJID: jid}) // creates the chat
	}
	if len(evs) > 0 {
		if err := p.writer.SubmitLive(ctx, evs); err != nil {
			return chatlist.Row{}, fmt.Errorf("starting the chat: %w", err)
		}
	}
	if !ok {
		if id, ok, err = p.reader.ChatID(ctx, jid); err != nil {
			return chatlist.Row{}, err
		} else if !ok {
			return chatlist.Row{}, errors.New("the chat could not be created")
		}
	}
	c, err := p.chatOf(ctx, id)
	if err != nil {
		return chatlist.Row{}, err
	}
	if c.Kind == domain.ChatDirect {
		// A person met only now has no name in the cache yet.
		if found, err := p.reader.IdentitiesByID(ctx, []domain.IdentityID{c.PeerID}); err == nil {
			p.names.set(found, nil)
		}
	}
	return p.row(&c), nil
}

// addressOf returns the address of the person query names: a phone
// number, looked up on the server, or the name of a contact. The events
// link the address forms the lookup found.
func (p *pipeline) addressOf(ctx context.Context, query string) (domain.JID, []domain.Event, error) {
	phone, isNumber, err := phoneNumber(query)
	switch {
	case err != nil:
		return "", nil, err
	case isNumber:
		r, ok := p.opts.Backend.(backend.NumberResolver)
		if !ok {
			return "", nil, errors.New("starting a chat by number needs a WhatsApp connection")
		}
		jid, evs, err := r.ResolveNumber(ctx, phone)
		switch {
		case errors.Is(err, backend.ErrNotOnWhatsApp):
			return "", nil, fmt.Errorf("+%s is not on WhatsApp", phone)
		case errors.Is(err, backend.ErrNotConnected):
			return "", nil, errors.New("not connected: try again when tarcin is online")
		case err != nil:
			return "", nil, err
		}
		return jid, evs, nil
	}
	found, err := p.reader.Contacts(ctx, query, contactChoices+1)
	if err != nil {
		return "", nil, err
	}
	if len(found) > 1 {
		// A name typed in full picks its contact among longer ones.
		key := textfold.Fold(strings.TrimSpace(query))
		for i := range found {
			if textfold.Fold(found[i].ContactName) == key {
				found = found[i : i+1]
				break
			}
		}
	}
	switch {
	case len(found) == 0:
		return "", nil, fmt.Errorf("no contact matches %q; for someone else, type the number with its country code", query)
	case len(found) > 1:
		names := make([]string, 0, contactChoices)
		for i := range found[:min(len(found), contactChoices)] {
			names = append(names, found[i].ContactName)
		}
		more := ""
		if len(found) > contactChoices {
			more = ", …"
		}
		return "", nil, fmt.Errorf("several contacts match: %s%s (tab completes)", strings.Join(names, ", "), more)
	}
	return found[0].CanonicalJID(), nil, nil
}

// phoneNumber reads s as a phone number in international format: an
// optional "+" or "00", then digits, which spaces, dashes, dots and
// parentheses may separate. isNumber is false for anything else, such
// as a name. A number without its country code is an error.
func phoneNumber(s string) (digits string, isNumber bool, err error) {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0, r == ' ', r == '-', r == '.', r == '(', r == ')':
		default:
			return "", false, nil
		}
	}
	d := b.String()
	if d == "" {
		return "", false, nil
	}
	if !strings.HasPrefix(s, "+") {
		if rest, ok := strings.CutPrefix(d, "00"); ok {
			d = rest
		} else if strings.HasPrefix(d, "0") {
			return "", true, errors.New("type the number with its country code, such as +90 555 123 45 67")
		}
	}
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return "", true, fmt.Errorf("%q is not a phone number in international format, such as +90 555 123 45 67", s)
	}
	return d, true, nil
}
