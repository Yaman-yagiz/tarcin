// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"sync"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// names is the display-name cache of every known person. The UI reads it
// from View, so it answers from memory; the pipeline keeps it current
// from the identities each commit reports (data-model.md §2: names are
// resolved at render time, never frozen into messages).
type names struct {
	mu   sync.RWMutex
	byID map[domain.IdentityID]string
}

func newNames() *names { return &names{byID: map[domain.IdentityID]string{}} }

// DisplayName implements msglist.Names.
func (n *names) DisplayName(id domain.IdentityID) string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.byID[id]
}

// set stores the names of the people found. Asked-for people who were
// not found were merged into someone else and are forgotten.
func (n *names) set(found []domain.Identity, asked []domain.IdentityID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, id := range asked {
		delete(n.byID, id)
	}
	for i := range found {
		n.byID[found[i].ID] = displayName(&found[i])
	}
}

func displayName(id *domain.Identity) string {
	if id.IsMe {
		return "You"
	}
	return id.DisplayName()
}
