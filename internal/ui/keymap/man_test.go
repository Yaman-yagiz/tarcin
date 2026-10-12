// SPDX-License-Identifier: GPL-3.0-or-later

package keymap

import (
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestManPageListsEveryBinding keeps tarcin(5) in step with the default
// key bindings.
func TestManPageListsEveryBinding(t *testing.T) {
	b, err := os.ReadFile("../../../docs/man/tarcin.5.scd")
	if err != nil {
		t.Fatal(err)
	}
	man := string(b)
	specs := Defaults()
	for _, c := range Contexts {
		if !strings.Contains(man, "*[keys."+string(c)+"]*") {
			t.Errorf("tarcin(5) lacks [keys.%s]", c)
		}
		for _, a := range slices.Sorted(maps.Keys(specs[c])) {
			quoted := make([]string, len(specs[c][a]))
			for i, k := range specs[c][a] {
				quoted[i] = `"` + k + `"`
			}
			if want := "*" + string(a) + "* = [" + strings.Join(quoted, ", ") + "]"; !strings.Contains(man, want) {
				t.Errorf("tarcin(5) lacks %s", want)
			}
		}
	}
}
