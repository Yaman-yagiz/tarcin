// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"os"
	"strings"
	"testing"
)

// TestManPageListsEveryKey keeps tarcin(5) in step with the settings.
func TestManPageListsEveryKey(t *testing.T) {
	b, err := os.ReadFile("../../docs/man/tarcin.5.scd")
	if err != nil {
		t.Fatal(err)
	}
	man := string(b)
	var out strings.Builder
	d := Default()
	if err := Write(&out, &d, nil); err != nil {
		t.Fatal(err)
	}
	n := 0
	for line := range strings.Lines(out.String()) {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			if !strings.Contains(man, "*"+line+"*") {
				t.Errorf("tarcin(5) lacks the section %s", line)
			}
		case line != "" && !strings.HasPrefix(line, "#"):
			key, value, _ := strings.Cut(line, " = ")
			n++
			if !strings.Contains(man, "*"+key+"* = _"+value+"_") {
				t.Errorf("tarcin(5) lacks %s = %s", key, value)
			}
		}
	}
	if n < 12 || !strings.Contains(man, "*[media.openers]*") {
		t.Fatalf("checked %d keys", n)
	}
}
