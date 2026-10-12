// SPDX-License-Identifier: GPL-3.0-or-later

package archtest

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// TestSourceHasNoInvisibleCharacters rejects Go files containing format
// characters (bidi controls, zero-width characters, BOM), combining marks
// or control characters other than tab and newline. Such characters make
// source text differ from what reviewers see ("Trojan Source",
// CVE-2021-42574). Test data must spell them as escapes such as \u202e.
func TestSourceHasNoInvisibleCharacters(t *testing.T) {
	root, _ := moduleRoot(t)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "build") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		data, err := os.ReadFile(p) //nolint:gosec // walking the module's own files
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		for i, line := range strings.Split(string(data), "\n") {
			if bad := invisible(line); bad != "" {
				t.Errorf("%s:%d: contains %s; write it as an escape", rel, i+1, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func invisible(line string) string {
	for _, r := range line {
		switch {
		case r == '\t':
		case unicode.Is(unicode.Cf, r), unicode.Is(unicode.Mn, r), unicode.IsControl(r):
			return fmt.Sprintf("U+%04X", r)
		}
	}
	return ""
}

func TestInvisibleDetector(t *testing.T) {
	for line, want := range map[string]string{
		"plain ascii\t":           "",
		"Türkçe ışık 👍 田中":        "",
		"x := \"\\u202e\"":        "", // an escape is fine
		"a\u202eb":                "U+202E",
		"zero\u200bwidth":         "U+200B",
		"bom\ufeff":               "U+FEFF",
		"e\u0301":                 "U+0301",
		"carriage\rreturn":        "U+000D",
		"family \U0001f468\u200d": "U+200D",
	} {
		if got := invisible(line); got != want {
			t.Errorf("invisible(%q) = %q, want %q", line, got, want)
		}
	}
}
