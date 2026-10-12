// SPDX-License-Identifier: GPL-3.0-or-later

package archtest

import (
	"strings"
	"testing"
)

// TestRulesDetectViolations proves that every rule fires on a synthetic
// module graph, so that a passing TestDependencyRules is meaningful.
func TestRulesDetectViolations(t *testing.T) {
	bad := imports{
		"internal/domain":      {"time", "internal/store", "github.com/google/uuid"},
		"internal/ui/msglist":  {"internal/store", "go.mau.fi/whatsmeow/types"},
		"internal/service":     {"internal/ui", "charm.land/bubbletea/v2"},
		"internal/store":       {"internal/app", "go.mau.fi/whatsmeow"},
		"internal/jobs":        {"github.com/mattn/go-sqlite3"},
		"internal/ui/chatlist": {"github.com/godbus/dbus/v5"},
		"internal/paths":       {"os", "golang.org/x/sys/unix"},
		"internal/logging":     {"log/slog", "internal/domain"},
		"internal/config":      {"github.com/BurntSushi/toml", "internal/store"},
		"internal/wa":          {"internal/store"},
	}
	want := map[string]int{
		"domain is pure: standard library only":                                    2,
		"paths is a leaf: standard library only":                                   1,
		"logging is a leaf: standard library only":                                 1,
		"jobs is a leaf: standard library only":                                    1,
		"the config stays below everything it configures":                          1,
		"ui does not reach into protocol or storage":                               2,
		"only app wires the ui":                                                    1,
		"only the command imports app":                                             1,
		"whatsmeow stays inside the adapter":                                       2,
		"the store stays below ui, app and the protocol adapter":                   1,
		"the adapter stays beside the store: wa imports neither store, ui nor app": 1,
		"the SQLite driver stays inside the store":                                 1,
		"Bubble Tea stays in the ui and its composition root":                      1,
		"D-Bus stays in the notifier":                                              1,
	}
	for _, r := range rules {
		got := r.check(bad)
		if len(got) != want[r.name] {
			t.Errorf("rule %q: got %d violations, want %d:\n%s", r.name, len(got), want[r.name], strings.Join(got, "\n"))
		}
	}
}

func TestRulesAllowValidGraph(t *testing.T) {
	good := imports{
		"cmd/tarcin":            {"internal/app", "internal/buildinfo"},
		"internal/app":          {"internal/ui", "internal/wa", "internal/store", "charm.land/bubbletea/v2"},
		"internal/domain":       {"time", "errors", "internal/domain/ids"},
		"internal/ui":           {"internal/ui/msglist", "internal/service", "internal/domain", "charm.land/lipgloss/v2"},
		"internal/ui/msglist":   {"internal/domain", "charm.land/bubbletea/v2"},
		"internal/wa":           {"go.mau.fi/whatsmeow", "go.mau.fi/whatsmeow/store/sqlstore", "internal/domain"},
		"internal/store":        {"github.com/mattn/go-sqlite3", "database/sql", "internal/domain", "internal/backend"},
		"internal/store/sqlite": {"github.com/mattn/go-sqlite3"},
	}
	for _, r := range rules {
		if got := r.check(good); len(got) != 0 {
			t.Errorf("rule %q flagged a valid graph:\n%s", r.name, strings.Join(got, "\n"))
		}
	}
}

func TestIsStdlib(t *testing.T) {
	for imp, want := range map[string]bool{
		"fmt":                 true,
		"net/http":            true,
		"internal/domain":     false,
		"cmd/tarcin":          false,
		"go.mau.fi/whatsmeow": false,
		"github.com/x/y":      false,
	} {
		if got := isStdlib(imp); got != want {
			t.Errorf("isStdlib(%q) = %v, want %v", imp, got, want)
		}
	}
}
