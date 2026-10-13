// SPDX-License-Identifier: GPL-3.0-or-later

package clipboard

import (
	"errors"
	"os/exec"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// fake replaces the seams: env is the environment, have the programs in
// $PATH, fail the ones that exit with an error.
func fake(t *testing.T, env map[string]string, have, fail []string) *[][]string {
	t.Helper()
	var ran [][]string
	oldEnv, oldLook, oldRun := getenv, lookPath, run
	t.Cleanup(func() { getenv, lookPath, run = oldEnv, oldLook, oldRun })
	getenv = func(k string) string { return env[k] }
	lookPath = func(name string) (string, error) {
		if slices.Contains(have, name) {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	run = func(argv []string, text string) error {
		ran = append(ran, append(slices.Clone(argv), text))
		if slices.Contains(fail, argv[0]) {
			return errors.New("exit status 1")
		}
		return nil
	}
	return &ran
}

func TestCopyUsesTheDisplaysTool(t *testing.T) {
	ran := fake(t, map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, []string{"wl-copy", "xclip"}, nil)
	if msg := Copy("merhaba")(); msg != (ResultMsg{Tool: "wl-copy"}) {
		t.Fatalf("msg = %#v", msg)
	}
	if want := [][]string{{"/usr/bin/wl-copy", "merhaba"}}; !slices.EqualFunc(*ran, want, slices.Equal) {
		t.Fatalf("ran %q, want %q", *ran, want)
	}

	// X11 only, and xclip fails: xsel is next.
	ran = fake(t, map[string]string{"DISPLAY": ":0"}, []string{"wl-copy", "xclip", "xsel"}, []string{"/usr/bin/xclip"})
	if msg := Copy("x")(); msg != (ResultMsg{Tool: "xsel"}) {
		t.Fatalf("msg = %#v", msg)
	}
	if len(*ran) != 2 || (*ran)[0][1] != "-selection" || (*ran)[1][1] != "--clipboard" {
		t.Fatalf("ran %q", *ran)
	}
}

func TestCopyFallsBackToTheTerminal(t *testing.T) {
	// Over SSH there is no display, and no program is tried.
	ran := fake(t, map[string]string{}, []string{"wl-copy", "xclip"}, nil)
	batch, ok := Copy("x")().(tea.BatchMsg)
	if !ok || len(batch) != 2 || len(*ran) != 0 {
		t.Fatalf("got %#v after running %q", batch, *ran)
	}
	if msg := batch[1](); msg != (ResultMsg{}) {
		t.Fatalf("the result names no tool: %#v", msg)
	}
}
