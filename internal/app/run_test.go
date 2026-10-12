// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Yaman-yagiz/tarcin/internal/paths"
	"github.com/Yaman-yagiz/tarcin/internal/wa"
)

// testProfile points every tarcin directory into a temporary directory
// and returns the default profile's paths.
func testProfile(t *testing.T) paths.Paths {
	t.Helper()
	root := t.TempDir()
	for _, v := range []string{"CONFIG", "DATA", "STATE", "CACHE"} {
		t.Setenv("TARCIN_"+v+"_DIR", filepath.Join(root, strings.ToLower(v)))
	}
	p, err := paths.Default(paths.DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// lockedBuffer collects a program's output.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return ansi.Strip(b.buf.String())
}

// TestRunOpensOnTheNotice starts tarcin with a device that is not linked
// yet. It must open on the first-run notice and not connect before the
// notice is accepted; quitting there leaves a usable profile.
func TestRunOpensOnTheNotice(t *testing.T) {
	p := testProfile(t)
	in, w := io.Pipe()
	defer func() { _ = w.Close() }()
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), in, out, RunOptions{
			ProgramOptions: []tea.ProgramOption{tea.WithWindowSize(120, 40)},
		})
	}()
	waitUntil(t, "the notice", func() bool { return strings.Contains(out.text(), "unofficial client") })
	if _, err := paths.AcquireLock(p.LockFile()); !errors.Is(err, paths.ErrLocked) {
		t.Fatalf("a second tarcin must be refused, got %v", err)
	}
	if err := Run(context.Background(), strings.NewReader(""), io.Discard, RunOptions{}); !errors.Is(err, paths.ErrLocked) {
		t.Fatalf("Run while running: %v", err)
	}
	if strings.Contains(out.text(), "Connecting") {
		t.Fatal("tarcin must not connect before the notice is accepted")
	}
	if _, err := w.Write([]byte("\x11")); err != nil { // ctrl+q
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("tarcin did not quit")
	}
	for _, f := range []string{p.SessionDB(), p.MessageDB()} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
	}
	lock, err := paths.AcquireLock(p.LockFile())
	if err != nil {
		t.Fatalf("the lock must be released: %v", err)
	}
	_ = lock.Release()
	log, err := os.ReadFile(p.LogFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tarcin started", "linked=false", "profile=default", "message store closed", "tarcin stopped"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("the log lacks %q:\n%s", want, log)
		}
	}
	if info, err := os.Stat(p.LogFile()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %v, %v", info.Mode(), err)
	}
}

func TestOpenProfileFixesPermissions(t *testing.T) {
	p := testProfile(t)
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p.ProfileData(), 0o755); err != nil { //nolint:gosec // a loose mode to fix
		t.Fatal(err)
	}
	var warn bytes.Buffer
	lock, err := openProfile(p, &warn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if !strings.Contains(warn.String(), p.ProfileData()) || !strings.Contains(warn.String(), "fixed") {
		t.Fatalf("warning = %q", warn.String())
	}
	if info, err := os.Stat(p.ProfileData()); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
}

func TestRemoveLeftovers(t *testing.T) {
	p := testProfile(t)
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Drafts(), p.ProfileState()) {
		t.Fatalf("drafts in %s, not in the profile's state %s", p.Drafts(), p.ProfileState())
	}
	draft := filepath.Join(p.Drafts(), "tarcin-draft-1")
	if err := os.MkdirAll(draft, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path string) {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(draft, "message.txt"))
	for _, name := range []string{downloadTemp + "1", uploadTemp + "2", adoptTemp + "3", "0a1b.jpg"} {
		write(filepath.Join(p.MediaCache(), name))
	}
	n, err := removeLeftovers(p)
	if err != nil || n != 4 {
		t.Fatalf("removed %d, err %v", n, err)
	}
	left, _ := os.ReadDir(p.MediaCache())
	if len(left) != 1 || left[0].Name() != "0a1b.jpg" {
		t.Fatalf("cache holds %v; only the temporary files go", left)
	}
	if left, _ := os.ReadDir(p.Drafts()); len(left) != 0 {
		t.Fatalf("drafts hold %v", left)
	}
	if n, err := removeLeftovers(p); err != nil || n != 0 {
		t.Fatalf("a second run removed %d, err %v", n, err)
	}
}

func TestHomeRelative(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	for path, want := range map[string]string{
		"/home/me/.local/share/tarcin": "~/.local/share/tarcin", "/home/meow/x": "/home/meow/x", "/srv/tarcin": "/srv/tarcin",
	} {
		if got := homeRelative(path); got != want {
			t.Errorf("homeRelative(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDeviceVersion(t *testing.T) {
	for v, want := range map[string][3]uint32{
		"0.1.0": {0, 1, 0}, "1.22.333-rc.1": {1, 22, 333}, "devel": {}, "1.2": {}, "1.x.3": {},
	} {
		if got := deviceVersion(v); got != want {
			t.Errorf("deviceVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

// newTestProfileData creates a profile with a session store, a message
// database and a cached media file.
func newTestProfileData(t *testing.T, p paths.Paths) {
	t.Helper()
	lock, err := openProfile(p, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	sess, err := openSession(t.Context(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.Close()
	pipe, err := openPipeline(t.Context(), p.MessageDB(), pipelineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = pipe.close(t.Context())
	if err := os.WriteFile(filepath.Join(p.MediaCache(), "photo.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLogoutWithoutData(t *testing.T) {
	testProfile(t)
	var out bytes.Buffer
	if err := Logout(t.Context(), strings.NewReader(""), &out, LogoutOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Nothing to log out of") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestLogoutUnlinksAndDeletes(t *testing.T) {
	p := testProfile(t)
	newTestProfileData(t, p)
	unlinked := 0
	o := LogoutOptions{
		linked: func(*wa.Session) bool { return true },
		unlink: func(context.Context, *wa.Session) error { unlinked++; return nil },
	}
	var out bytes.Buffer
	if err := Logout(t.Context(), strings.NewReader("y\n"), &out, o); err != nil {
		t.Fatal(err)
	}
	if unlinked != 1 || !strings.Contains(out.String(), "unlinked and its keys are deleted") {
		t.Fatalf("unlinked %d times; out = %q", unlinked, out.String())
	}
	for _, f := range []string{p.SessionDB(), p.MessageDB(), p.MessageDB() + "-wal", p.MediaCache()} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s must be deleted: %v", f, err)
		}
	}
}

func TestLogoutKeepsMessagesByDefault(t *testing.T) {
	p := testProfile(t)
	newTestProfileData(t, p)
	var out bytes.Buffer
	if err := Logout(t.Context(), strings.NewReader(""), &out, LogoutOptions{}); err != nil { // end of input: no
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No device was linked") || !strings.Contains(out.String(), "Your messages stay in") {
		t.Fatalf("out = %q", out.String())
	}
	if _, err := os.Stat(p.SessionDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the session store must be deleted")
	}
	if _, err := os.Stat(p.MessageDB()); err != nil {
		t.Fatalf("the messages must stay: %v", err)
	}
}

func TestLogoutOffline(t *testing.T) {
	p := testProfile(t)
	newTestProfileData(t, p)
	offline := LogoutOptions{
		linked: func(*wa.Session) bool { return true },
		unlink: func(context.Context, *wa.Session) error { return errors.New("no network") },
	}
	var out bytes.Buffer
	if err := Logout(t.Context(), strings.NewReader("n\n"), &out, offline); !errors.Is(err, ErrLogoutCanceled) {
		t.Fatalf("declining must cancel: %v", err)
	}
	if _, err := os.Stat(p.SessionDB()); err != nil {
		t.Fatalf("the keys must stay: %v", err)
	}
	out.Reset()
	if err := Logout(t.Context(), strings.NewReader("yes\nn\n"), &out, offline); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no network") || !strings.Contains(out.String(), "Linked devices") {
		t.Fatalf("out = %q", out.String())
	}
	if _, err := os.Stat(p.SessionDB()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the keys must be deleted when the user agrees")
	}
	if _, err := os.Stat(p.MessageDB()); err != nil {
		t.Fatalf("the messages must stay: %v", err)
	}
}

// TestEverythingRunWritesIsPrivate runs tarcin under a typical login
// umask, without the 0077 main sets, and checks every directory and file
// it created (security.md §3): directories 0700 and files 0600 by their
// own modes, and no drafts left.
func TestEverythingRunWritesIsPrivate(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	p := testProfile(t)
	in, w := io.Pipe()
	defer func() { _ = w.Close() }()
	out := &lockedBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), in, out, RunOptions{ProgramOptions: []tea.ProgramOption{tea.WithWindowSize(120, 40)}})
	}()
	waitUntil(t, "the notice", func() bool { return strings.Contains(out.text(), "unofficial client") })
	if _, err := w.Write([]byte("\x11")); err != nil { // ctrl+q
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, root := range []string{p.Data, p.State, p.Cache} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			want := fs.FileMode(0o600)
			if d.IsDir() {
				want = 0o700
			} else {
				files++
			}
			if info.Mode().Perm() != want {
				t.Errorf("%s: mode %04o, want %04o", path, info.Mode().Perm(), want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 4 { // the two databases, the lock and the log at least
		t.Fatalf("only %d files were checked", files)
	}
	if left, err := os.ReadDir(p.Drafts()); err != nil || len(left) != 0 {
		t.Fatalf("drafts left: %v, %v", left, err)
	}
}
