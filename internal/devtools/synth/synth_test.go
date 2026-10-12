// SPDX-License-Identifier: GPL-3.0-or-later

package synth

import (
	"strings"
	"testing"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

func TestDeterministic(t *testing.T) {
	a, b := New(42), New(42)
	for range 200 {
		if x, y := a.Message(), b.Message(); x != y {
			t.Fatalf("same seed diverged: %q vs %q", x, y)
		}
		if a.WAID() != b.WAID() || a.LID() != b.LID() || a.Name() != b.Name() {
			t.Fatal("same seed diverged on identifiers")
		}
	}
	if New(1).Message() == New(2).Message() && New(1).WAID() == New(2).WAID() {
		t.Fatal("different seeds produced identical output")
	}
}

func TestIdentifiersAreValid(t *testing.T) {
	g := New(7)
	for range 500 {
		for _, j := range []domain.JID{g.PN(), g.LID(), g.GroupJID()} {
			if _, err := domain.ParseJID(string(j)); err != nil {
				t.Fatalf("generated invalid JID: %v", err)
			}
		}
		if id := g.WAID(); len(id) != 20 || !strings.HasPrefix(string(id), "3EB0") {
			t.Fatalf("unexpected WAID %q", id)
		}
	}
	if !New(1).PN().IsPN() || !New(1).LID().IsLID() || !New(1).GroupJID().IsGroup() {
		t.Fatal("JID kinds are wrong")
	}
}

func TestSentenceBounds(t *testing.T) {
	g := New(3)
	for range 500 {
		n := len(strings.Fields(g.Sentence(2, 5)))
		if n < 2 || n > 5 {
			t.Fatalf("Sentence(2, 5) produced %d words", n)
		}
	}
	if got := len(strings.Fields(g.Sentence(3, 3))); got != 3 {
		t.Fatalf("Sentence(3, 3) produced %d words", got)
	}
}

func TestMessageVariety(t *testing.T) {
	g := New(9)
	var multiline, long int
	for range 2000 {
		m := g.Message()
		if m == "" {
			t.Fatal("empty message")
		}
		if strings.Contains(m, "\n") {
			multiline++
		}
		if len(strings.Fields(m)) > 30 {
			long++
		}
	}
	if multiline == 0 || long == 0 {
		t.Fatalf("distribution lacks variety: multiline=%d long=%d", multiline, long)
	}
}
