// SPDX-License-Identifier: GPL-3.0-or-later

// Package keymap binds key sequences to named actions per UI context, in
// the style of aerc and iamb (docs/architecture/ui.md §5). Bindings can be
// overridden from the configuration. Within a lookup chain no binding may
// be a prefix of another, so multi-key sequences such as "gg" resolve
// deterministically without timeouts.
package keymap

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Context is a set of bindings that applies while a part of the UI has
// focus.
type Context string

// Contexts. Lookups walk a chain: the chat list and the message view fall
// back to Navigation and then Global; the composer falls back to Global.
const (
	Global     Context = "global"
	Navigation Context = "navigation"
	ChatList   Context = "chatlist"
	Messages   Context = "messages"
	Compose    Context = "compose"
)

// Contexts lists every context, in configuration order.
var Contexts = []Context{Global, Navigation, ChatList, Messages, Compose}

// Chain returns the lookup chain for a focused context.
func Chain(c Context) []Context {
	switch c {
	case ChatList, Messages:
		return []Context{c, Navigation, Global}
	case Navigation:
		return []Context{Navigation, Global}
	case Compose:
		return []Context{Compose, Global}
	}
	return []Context{Global}
}

// Action is a named operation a binding triggers.
type Action string

// Actions, by the context that defines their default binding.
const (
	Quit       Action = "quit"
	Help       Action = "help"
	Command    Action = "command"
	Search     Action = "search"
	FocusNext  Action = "focus-next"
	FocusPrev  Action = "focus-prev"
	NextUnread Action = "next-unread"
	PrevUnread Action = "prev-unread"

	Down         Action = "down"
	Up           Action = "up"
	Top          Action = "top"
	Bottom       Action = "bottom"
	HalfPageDown Action = "half-page-down"
	HalfPageUp   Action = "half-page-up"
	PageDown     Action = "page-down"
	PageUp       Action = "page-up"

	Open Action = "open"

	ComposeMsg Action = "compose"
	Reply      Action = "reply"
	React      Action = "react"
	Edit       Action = "edit"
	Delete     Action = "delete"
	Copy       Action = "copy"
	OpenMedia  Action = "open-media"
	SaveMedia  Action = "save-media"
	JumpQuoted Action = "jump-quoted"
	OpenLink   Action = "open-link"
	ViewPager  Action = "view-pager"
	Retry      Action = "retry"
	Forward    Action = "forward"

	Send         Action = "send"
	Newline      Action = "newline"
	EditExternal Action = "edit-external"
	NormalMode   Action = "normal-mode"
)

// defaults maps each context to its actions and their default bindings.
// Alt chords are never the only binding (ui.md §5.2).
var defaults = map[Context]map[Action][]string{
	Global: {
		Quit:       {"ctrl+q"},
		Help:       {"?"},
		Command:    {":"},
		Search:     {"ctrl+k", "/"},
		FocusNext:  {"tab"},
		FocusPrev:  {"shift+tab"},
		NextUnread: {"]u"},
		PrevUnread: {"[u"},
	},
	Navigation: {
		Down:         {"j", "down"},
		Up:           {"k", "up"},
		Top:          {"gg", "home"},
		Bottom:       {"G", "end"},
		HalfPageDown: {"ctrl+d"},
		HalfPageUp:   {"ctrl+u"},
		PageDown:     {"ctrl+f", "pgdown"},
		PageUp:       {"ctrl+b", "pgup"},
	},
	ChatList: {
		Open: {"enter", "l"},
	},
	Messages: {
		ComposeMsg: {"i", "a"},
		Reply:      {"R"},
		React:      {"r"},
		Edit:       {"e"},
		Delete:     {"dd"},
		Copy:       {"y"},
		OpenMedia:  {"o"},
		SaveMedia:  {"s"},
		JumpQuoted: {"gq"},
		OpenLink:   {"gx"},
		ViewPager:  {"v"},
		Retry:      {"ctrl+r"},
		Forward:    {"f"},
	},
	Compose: {
		Send:         {"enter"},
		Newline:      {"shift+enter", "alt+enter", "ctrl+j"},
		EditExternal: {"ctrl+x ctrl+e"},
		NormalMode:   {"esc"},
	},
}

// Defaults returns a copy of the default bindings, as the configuration
// writes them: context → action → key sequences.
func Defaults() map[Context]map[Action][]string {
	out := make(map[Context]map[Action][]string, len(defaults))
	for c, actions := range defaults {
		out[c] = make(map[Action][]string, len(actions))
		for a, specs := range actions {
			out[c][a] = slices.Clone(specs)
		}
	}
	return out
}

// Binding is one key sequence bound to an action.
type Binding struct {
	Context Context
	Action  Action
	Keys    []string // canonical keys
}

// String renders the sequence as it would be typed, e.g. "g g".
func (b Binding) String() string { return strings.Join(b.Keys, " ") }

// Keymap is an immutable, validated set of bindings.
type Keymap struct {
	tries    map[Context]*node
	bindings []Binding
}

type node struct {
	next   map[string]*node
	action Action
}

// Default returns the built-in keymap.
func Default() *Keymap {
	km, err := New(nil)
	if err != nil {
		panic("keymap: invalid defaults: " + err.Error())
	}
	return km
}

// New builds a keymap from the defaults and the overrides from the
// configuration. An override replaces all bindings of an action; an empty
// list unbinds it. Errors name the context, the action and the key.
func New(overrides map[string]map[string][]string) (*Keymap, error) {
	effective := map[Context]map[Action][]string{}
	for c, actions := range defaults {
		effective[c] = maps.Clone(actions)
	}
	for _, cname := range slices.Sorted(maps.Keys(overrides)) {
		c := Context(cname)
		if _, ok := defaults[c]; !ok {
			return nil, fmt.Errorf("keys: unknown context %q (want one of %s)", cname, contextList())
		}
		for _, aname := range slices.Sorted(maps.Keys(overrides[cname])) {
			a := Action(aname)
			if _, ok := defaults[c][a]; !ok {
				return nil, fmt.Errorf("keys.%s: unknown action %q", cname, aname)
			}
			effective[c][a] = overrides[cname][aname]
		}
	}

	km := &Keymap{tries: map[Context]*node{}}
	for _, c := range Contexts {
		root := &node{}
		km.tries[c] = root
		for _, a := range slices.Sorted(maps.Keys(effective[c])) {
			for _, spec := range effective[c][a] {
				seq, err := ParseSequence(spec)
				if err != nil {
					return nil, fmt.Errorf("keys.%s.%s: %w", c, a, err)
				}
				if c == Compose && IsText(seq[0]) {
					return nil, fmt.Errorf("keys.%s.%s: %q starts with a text key, which the composer must receive", c, a, spec)
				}
				km.bindings = append(km.bindings, Binding{Context: c, Action: a, Keys: seq})
			}
		}
	}
	// Insert and check conflicts within every lookup chain.
	for _, b := range km.bindings {
		if err := km.insert(b); err != nil {
			return nil, err
		}
	}
	for _, c := range Contexts {
		if err := km.checkChain(Chain(c)); err != nil {
			return nil, err
		}
	}
	return km, nil
}

func contextList() string {
	names := make([]string, len(Contexts))
	for i, c := range Contexts {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

func (km *Keymap) insert(b Binding) error {
	n := km.tries[b.Context]
	for i, k := range b.Keys {
		if n.action != "" {
			return fmt.Errorf("keys.%s: %q (%s) starts with %q (%s) (prefix conflict)", b.Context, b, b.Action, strings.Join(b.Keys[:i], " "), n.action)
		}
		if n.next == nil {
			n.next = map[string]*node{}
		}
		child, ok := n.next[k]
		if !ok {
			child = &node{}
			n.next[k] = child
		}
		n = child
	}
	switch {
	case n.action == b.Action:
		return nil // the same binding listed twice
	case n.action != "":
		return fmt.Errorf("keys.%s: %q is bound to both %s and %s", b.Context, b, n.action, b.Action)
	case len(n.next) > 0:
		return fmt.Errorf("keys.%s: %q (%s) is a prefix of another binding", b.Context, b, b.Action)
	}
	n.action = b.Action
	return nil
}

// checkChain rejects bindings in one context of the chain that collide
// with or are a prefix of bindings in another, since lookups would then
// depend on the order of contexts in surprising ways.
func (km *Keymap) checkChain(chain []Context) error {
	for i, c := range chain {
		for _, b := range km.bindings {
			if b.Context != c {
				continue
			}
			for _, other := range chain[i+1:] {
				if a, st := km.lookup(other, b.Keys); st != none {
					if st == match {
						// An earlier context overriding a later one is allowed
						// only for the exact same sequence (e.g. "enter").
						continue
					}
					return fmt.Errorf("keys: %q (%s.%s) is a prefix of a binding in %s%s", b, c, b.Action, other, actionSuffix(a))
				}
				if km.isPrefixOf(other, b.Keys) {
					return fmt.Errorf("keys: %q (%s.%s) starts with a shorter binding in %s (prefix conflict)", b, c, b.Action, other)
				}
			}
		}
	}
	return nil
}

func actionSuffix(a Action) string {
	if a == "" {
		return ""
	}
	return " (" + string(a) + ")"
}

type status uint8

const (
	none status = iota
	prefix
	match
)

func (km *Keymap) lookup(c Context, seq []string) (Action, status) {
	n := km.tries[c]
	for _, k := range seq {
		n = n.next[k]
		if n == nil {
			return "", none
		}
	}
	if n.action != "" {
		return n.action, match
	}
	return "", prefix
}

// isPrefixOf reports whether some binding in c is a strict prefix of seq.
func (km *Keymap) isPrefixOf(c Context, seq []string) bool {
	n := km.tries[c]
	for _, k := range seq[:len(seq)-1] {
		n = n.next[k]
		if n == nil {
			return false
		}
		if n.action != "" {
			return true
		}
	}
	return false
}

// Bindings returns every binding, ordered by context and action, for the
// help screen and documentation.
func (km *Keymap) Bindings() []Binding { return slices.Clone(km.bindings) }
