// SPDX-License-Identifier: GPL-3.0-or-later

// Package archtest enforces the package dependency rules described in
// docs/architecture/overview.md §5. It contains tests only.
package archtest

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// rule restricts the imports of every package matching From.
// Patterns are module-relative ("internal/ui") or full import paths of
// third-party modules ("go.mau.fi/whatsmeow"); a pattern matches the path
// itself and everything below it.
type rule struct {
	name string
	// From selects the packages the rule applies to.
	From string
	// Deny lists import patterns that matching packages must not import.
	Deny []string
	// StdlibOnly forbids every import outside the standard library,
	// except imports of the package's own subtree.
	StdlibOnly bool
	// OnlyFrom, when set, inverts the rule: only packages matching one of
	// these patterns (or inside From itself) may import From.
	OnlyFrom []string
}

var rules = []rule{
	{
		name:       "domain is pure: standard library only",
		From:       "internal/domain",
		StdlibOnly: true,
	},
	{
		name:       "paths is a leaf: standard library only",
		From:       "internal/paths",
		StdlibOnly: true,
	},
	{
		name:       "jobs is a leaf: standard library only",
		From:       "internal/jobs",
		StdlibOnly: true,
	},
	{
		name:       "logging is a leaf: standard library only",
		From:       "internal/logging",
		StdlibOnly: true,
	},
	{
		name:       "diag is a leaf: standard library only",
		From:       "internal/diag",
		StdlibOnly: true,
	},
	{
		name: "the config stays below everything it configures",
		From: "internal/config",
		Deny: []string{"internal/ui", "internal/app", "internal/store", "internal/wa", "internal/backend"},
	},
	{
		name: "ui does not reach into protocol or storage",
		From: "internal/ui",
		Deny: []string{"internal/wa", "internal/store", "go.mau.fi/whatsmeow", "github.com/mattn/go-sqlite3"},
	},
	{
		name:     "only app wires the ui",
		From:     "internal/ui",
		OnlyFrom: []string{"internal/app"},
	},
	{
		name:     "only the command imports app",
		From:     "internal/app",
		OnlyFrom: []string{"cmd/tarcin"},
	},
	{
		name:     "whatsmeow stays inside the adapter",
		From:     "go.mau.fi/whatsmeow",
		OnlyFrom: []string{"internal/wa"},
	},
	{
		name: "the store stays below ui, app and the protocol adapter",
		From: "internal/store",
		Deny: []string{"internal/ui", "internal/app", "internal/wa", "internal/service", "internal/jobs"},
	},
	{
		name: "the adapter stays beside the store: wa imports neither store, ui nor app",
		From: "internal/wa",
		Deny: []string{"internal/store", "internal/ui", "internal/app", "internal/paths"},
	},
	{
		name:     "the SQLite driver stays inside the store",
		From:     "github.com/mattn/go-sqlite3",
		OnlyFrom: []string{"internal/store"},
	},
	{
		name:     "D-Bus stays in the notifier",
		From:     "github.com/godbus/dbus",
		OnlyFrom: []string{"internal/notify"},
	},
	{
		name:     "Bubble Tea stays in the ui and its composition root",
		From:     "charm.land",
		OnlyFrom: []string{"internal/ui", "internal/app"},
	},
}

// imports maps a module-relative package directory ("internal/ui") to the
// import paths used by its non-test files. Imports of the module itself
// are rewritten to module-relative form.
type imports map[string][]string

func (r rule) check(pkgs imports) []string {
	var out []string
	for pkg, imps := range pkgs {
		for _, imp := range imps {
			if v := r.violates(pkg, imp); v != "" {
				out = append(out, fmt.Sprintf("%s: %s imports %s (%s)", r.name, pkg, imp, v))
			}
		}
	}
	slices.Sort(out)
	return out
}

func (r rule) violates(pkg, imp string) string {
	switch {
	case r.OnlyFrom != nil:
		if !within(imp, r.From) || within(pkg, r.From) {
			return ""
		}
		for _, allowed := range r.OnlyFrom {
			if within(pkg, allowed) {
				return ""
			}
		}
		return "not allowed to import " + r.From
	case !within(pkg, r.From):
		return ""
	case r.StdlibOnly && !isStdlib(imp) && !within(imp, r.From):
		return "only the standard library is allowed"
	}
	for _, d := range r.Deny {
		if within(imp, d) {
			return "denied"
		}
	}
	return ""
}

func within(p, pattern string) bool {
	return p == pattern || strings.HasPrefix(p, pattern+"/")
}

// isStdlib reports whether an import path belongs to the standard library.
// Module-relative paths of this module ("internal/…", "cmd/…") have no dot
// either, so callers pass only paths already classified by load.
func isStdlib(imp string) bool {
	if within(imp, "internal") || within(imp, "cmd") {
		return false
	}
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

// load parses the imports of every non-test Go file in the module.
func load(t *testing.T, root, module string) imports {
	t.Helper()
	pkgs := imports{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "tools" || name == "build") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		for _, spec := range f.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if within(imp, module) {
				imp = strings.TrimPrefix(strings.TrimPrefix(imp, module), "/")
			}
			pkgs[pkg] = append(pkgs[pkg], imp)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module: %v", err)
	}
	return pkgs
}

// moduleRoot finds the module from the package's directory, where go
// test runs the tests; the test file's own path is no help under
// -trimpath, which packagers build with.
func moduleRoot(t *testing.T) (root, module string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // path derived from the test's own directory
		if err == nil {
			for line := range strings.Lines(string(data)) {
				if m, ok := strings.CutPrefix(line, "module "); ok {
					return dir, strings.TrimSpace(m)
				}
			}
			t.Fatalf("no module line in %s", filepath.Join(dir, "go.mod"))
		}
		if dir == filepath.Dir(dir) {
			t.Fatal("go.mod not found")
		}
	}
}

func TestDependencyRules(t *testing.T) {
	root, module := moduleRoot(t)
	pkgs := load(t, root, module)
	if _, ok := pkgs["cmd/tarcin"]; !ok {
		t.Fatalf("loader found no cmd/tarcin package; found %v", keys(pkgs))
	}
	for _, r := range rules {
		for _, v := range r.check(pkgs) {
			t.Error(v)
		}
	}
}

func keys(m imports) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
