// SPDX-License-Identifier: GPL-3.0-or-later

package keymap

// ActionMsg delivers a key-bound action to the focused pane.
type ActionMsg struct {
	Action Action
}

// Result is the outcome of feeding one key to a Matcher.
type Result struct {
	// Action is set when the key completed a binding.
	Action Action
	// Pending is true when the key started or continued a sequence that is
	// not complete yet; the key is consumed.
	Pending bool
}

// Handled reports whether the key was consumed by the keymap. Unhandled
// keys go to the focused pane, e.g. as text for the composer.
func (r Result) Handled() bool { return r.Action != "" || r.Pending }

// Matcher resolves keys to actions, keeping the keys of an incomplete
// sequence between calls. It is owned by the root model.
type Matcher struct {
	km      *Keymap
	pending []string
}

// NewMatcher returns a matcher for km.
func NewMatcher(km *Keymap) *Matcher { return &Matcher{km: km} }

// Pending returns the keys of the incomplete sequence, for display.
func (m *Matcher) Pending() []string { return m.pending }

// Reset drops an incomplete sequence, e.g. when focus changes.
func (m *Matcher) Reset() { m.pending = m.pending[:0] }

// Feed handles one canonical key (KeyPressMsg.String) in the focused
// context. In the composer, text keys bypass global bindings so that, for
// example, "?" is typed rather than opening the help.
func (m *Matcher) Feed(focused Context, key string) Result {
	m.pending = append(m.pending, key)
	if r, ok := m.resolve(focused); ok {
		return r
	}
	// The sequence cannot complete: drop it and retry the key on its own,
	// as a fresh start ("g" then "j" moves down).
	if len(m.pending) > 1 {
		m.pending = append(m.pending[:0], key)
		if r, ok := m.resolve(focused); ok {
			return r
		}
	}
	m.Reset()
	return Result{}
}

func (m *Matcher) resolve(focused Context) (Result, bool) {
	chain := Chain(focused)
	pending := false
	for _, c := range chain {
		if focused == Compose && c != Compose && IsText(m.pending[0]) {
			continue
		}
		switch a, st := m.km.lookup(c, m.pending); st {
		case match:
			m.Reset()
			return Result{Action: a}, true
		case prefix:
			pending = true
		}
	}
	if pending {
		return Result{Pending: true}, true
	}
	return Result{}, false
}
