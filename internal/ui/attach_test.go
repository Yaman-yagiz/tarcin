// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/ui/chatlist"
)

func TestCompletePath(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"photo.jpg", "piano.pdf", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "photos"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		arg, want string
		choices   []string
	}{
		{dir + "/p", dir + "/p", []string{"photo.jpg", "photos/", "piano.pdf"}},
		{dir + "/pho", dir + "/photo", []string{"photo.jpg", "photos/"}},
		{dir + "/pia", dir + "/piano.pdf", []string{"piano.pdf"}},
		{dir + "/photos", dir + "/photos", []string{"photos/"}},
		{dir + "/.h", dir + "/.hidden", []string{".hidden"}},
		{dir + "/zzz", dir + "/zzz", nil},
	} {
		got, choices := completePath(tt.arg)
		if tt.arg == dir+"/photos" {
			tt.want = dir + "/photos/"
		}
		if got != tt.want || !slices.Equal(choices, tt.choices) {
			t.Errorf("completePath(%q) = %q %v, want %q %v", tt.arg, got, choices, tt.want, tt.choices)
		}
	}
	t.Setenv("HOME", dir)
	if got, _ := completePath("~/pia"); got != "~/piano.pdf" {
		t.Fatalf("~ is kept: %q", got)
	}
}

func TestAttachCommand(t *testing.T) {
	var got []Outgoing
	var paths []string
	fail := false
	m := New(Options{History: emptyHistory{}, Send: func(Outgoing) tea.Cmd { return nil },
		Attach: func(out Outgoing, path string) tea.Cmd {
			got, paths = append(got, out), append(paths, path)
			return func() tea.Msg {
				if fail {
					return AttachResultMsg{Chat: out.Chat, Err: errors.New("dosya.pdf is empty")}
				}
				return AttachResultMsg{Chat: out.Chat}
			}
		}})
	size(m, 120, 30)
	command := func(line string) {
		m.Update(key(":"))
		typeKeys(m, line)
		_, cmd := m.Update(key("enter"))
		runCmd(m, cmd)
	}
	command("attach /x")
	if !strings.Contains(statusOf(m), "open a chat") {
		t.Fatalf("status %q", statusOf(m))
	}
	m.Update(chatlist.LoadMsg{Rows: []chatlist.Row{{ID: 4, Title: "Aile", LastActivity: time.Now()}}})
	m.Update(chatlist.OpenMsg{ID: 4})
	m.comp.SetChat(4)
	m.Update(key("i"))
	typeKeys(m, "bak")
	m.Update(key("esc"))
	fail = true
	command("attach /tmp/dosya.pdf")
	if !strings.Contains(statusOf(m), "Not sent: dosya.pdf is empty") || m.comp.Value() != "bak" {
		t.Fatalf("a failure keeps the caption: %q, %q", statusOf(m), m.comp.Value())
	}
	fail = false
	command("attach /tmp/kedi.jpg")
	if len(got) != 2 || got[1].Text != "bak" || got[1].Chat != 4 || paths[1] != "/tmp/kedi.jpg" || m.comp.Value() != "" {
		t.Fatalf("sent %+v %v; composer %q", got, paths, m.comp.Value())
	}
	command("attach")
	if !strings.Contains(statusOf(m), "usage: :attach PATH") {
		t.Fatalf("status %q", statusOf(m))
	}
	// Tab completes the path on the command line.
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "rapor.pdf"), nil, 0o600)
	m.Update(key(":"))
	typeKeys(m, "attach "+dir+"/ra")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	runCmd(m, cmd)
	if v := m.input.Value(); v != "attach "+dir+"/rapor.pdf" {
		t.Fatalf("completed %q", v)
	}
	if completeAttach("open x") != nil {
		t.Fatal("only :attach completes paths")
	}
}
