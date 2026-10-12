// SPDX-License-Identifier: GPL-3.0-or-later

package paths

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestResolveDefaults(t *testing.T) {
	p, err := Resolve(env(nil), "/home/ali", DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	want := Paths{
		Profile: "default",
		Config:  "/home/ali/.config/tarcin",
		Data:    "/home/ali/.local/share/tarcin",
		State:   "/home/ali/.local/state/tarcin",
		Cache:   "/home/ali/.cache/tarcin",
	}
	if p != want {
		t.Fatalf("Resolve = %+v, want %+v", p, want)
	}
	for got, want := range map[string]string{
		p.SessionDB():    "/home/ali/.local/share/tarcin/profiles/default/session.db",
		p.MessageDB():    "/home/ali/.local/share/tarcin/profiles/default/tarcin.db",
		p.LockFile():     "/home/ali/.local/share/tarcin/profiles/default/lock",
		p.ProfileState(): "/home/ali/.local/state/tarcin/profiles/default",
		p.LogFile():      "/home/ali/.local/state/tarcin/tarcin.log",
		p.MediaCache():   "/home/ali/.cache/tarcin/profiles/default/media",
		p.ConfigFile():   "/home/ali/.config/tarcin/config.toml",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

func TestResolveXDGVariables(t *testing.T) {
	p, err := Resolve(env(map[string]string{
		"XDG_CONFIG_HOME": "/cfg",
		"XDG_DATA_HOME":   "relative/data", // ignored, as the spec says
		"XDG_STATE_HOME":  "",
		"XDG_CACHE_HOME":  "/var/cache/ali/",
		"XDG_RUNTIME_DIR": "/run/user/1000",
	}), "/home/ali", "work")
	if err != nil {
		t.Fatal(err)
	}
	if p.Config != "/cfg/tarcin" || p.Data != "/home/ali/.local/share/tarcin" || p.State != "/home/ali/.local/state/tarcin" ||
		p.Cache != "/var/cache/ali/tarcin" || p.Runtime != "/run/user/1000/tarcin" || p.Profile != "work" {
		t.Fatalf("Resolve = %+v", p)
	}
	p, _ = Resolve(env(map[string]string{"XDG_RUNTIME_DIR": "run"}), "/home/ali", "x")
	if p.Runtime != "" {
		t.Fatalf("a relative runtime dir must be ignored: %q", p.Runtime)
	}
}

func TestDrafts(t *testing.T) {
	p, _ := Resolve(env(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}), "/home/ali", "work")
	if d := p.Drafts(); d != "/home/ali/.local/state/tarcin/profiles/work/drafts" {
		t.Fatalf("Drafts = %q; per profile, in its state directory", d)
	}
}

func TestResolveOverrides(t *testing.T) {
	p, err := Resolve(env(map[string]string{
		"TARCIN_DATA_DIR": "/tmp/t/data/", "XDG_DATA_HOME": "/ignored",
		"TARCIN_CONFIG_DIR": "/tmp/t/config", "TARCIN_STATE_DIR": "/tmp/t/state", "TARCIN_CACHE_DIR": "/tmp/t/cache",
	}), "/home/ali", DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if p.Data != "/tmp/t/data" || p.Config != "/tmp/t/config" || p.State != "/tmp/t/state" || p.Cache != "/tmp/t/cache" {
		t.Fatalf("Resolve = %+v", p)
	}
	if _, err := Resolve(env(map[string]string{"TARCIN_DATA_DIR": "data"}), "/home/ali", DefaultProfile); err == nil ||
		!strings.Contains(err.Error(), "TARCIN_DATA_DIR") {
		t.Fatalf("a relative override must be refused: %v", err)
	}
}

func TestResolveRefusesBadInput(t *testing.T) {
	for _, profile := range []string{"", "..", "../x", "a/b", "Work", "-x", "_x", strings.Repeat("a", 33), "ünlü", "a b"} {
		if _, err := Resolve(env(nil), "/home/ali", profile); !errors.Is(err, ErrBadProfile) {
			t.Errorf("profile %q: err = %v", profile, err)
		}
	}
	for _, profile := range []string{"default", "work-2", "a_b", strings.Repeat("a", 32), "0"} {
		if _, err := Resolve(env(nil), "/home/ali", profile); err != nil {
			t.Errorf("profile %q: %v", profile, err)
		}
	}
	if _, err := Resolve(env(nil), "home/ali", DefaultProfile); err == nil {
		t.Error("a relative home must be refused")
	}
}

func testPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	p, err := Resolve(env(nil), root, DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestEnsureCreatesPrivateDirectories(t *testing.T) {
	old := syscall.Umask(0o022) // a typical login umask: Ensure must not rely on SetUmask
	defer syscall.Umask(old)
	p := testPaths(t)
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range append(p.privateDirs(), filepath.Dir(p.Data)) {
		if m := mode(t, d); m != 0o700 {
			t.Errorf("%s: mode %04o, want 0700", d, m)
		}
	}
	if _, err := os.Stat(p.Config); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the config directory is the user's and must not be created")
	}
	if problems, err := p.Check(); err != nil || len(problems) != 0 {
		t.Fatalf("Check = %v, %v", problems, err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal("Ensure must be idempotent")
	}
}

func TestCheckAndFix(t *testing.T) {
	p := testPaths(t)
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p.ProfileData(), 0o755); err != nil { //nolint:gosec // the loose mode under test
		t.Fatal(err)
	}
	for path, m := range map[string]fs.FileMode{p.MessageDB(): 0o644, p.SessionDB() + "-wal": 0o640, p.LockFile(): 0o600} {
		if err := os.WriteFile(path, nil, m); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, m); err != nil { // not subject to the umask
			t.Fatal(err)
		}
	}
	problems, err := p.Check()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Problem{}
	for _, pr := range problems {
		got[pr.Path] = pr
	}
	if len(got) != 3 || got[p.ProfileData()].Want != 0o700 || got[p.MessageDB()].Mode != 0o644 ||
		got[p.SessionDB()+"-wal"].Want != 0o600 || got[p.MessageDB()].Foreign {
		t.Fatalf("problems = %+v", problems)
	}
	if s := got[p.MessageDB()].String(); !strings.Contains(s, "0644") || !strings.Contains(s, "0600") {
		t.Errorf("String() = %q", s)
	}
	left, err := Fix(problems)
	if err != nil || len(left) != 0 {
		t.Fatalf("Fix = %v, %v", left, err)
	}
	if problems, _ := p.Check(); len(problems) != 0 {
		t.Fatalf("after Fix: %+v", problems)
	}
	foreign := Problem{Path: "/x", Owner: 0, Foreign: true}
	if left, _ := Fix([]Problem{foreign}); len(left) != 1 || !strings.Contains(left[0].String(), "owned by user 0") {
		t.Fatalf("a path owned by someone else cannot be fixed: %v", left)
	}
}

func TestSetUmask(t *testing.T) {
	old := SetUmask()
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o666); err != nil { //nolint:gosec // the umask must narrow this
		t.Fatal(err)
	}
	if m := mode(t, path); m != 0o600 {
		t.Fatalf("mode %04o under umask 0077, want 0600", m)
	}
}

func TestLockExcludesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	l, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := mode(t, path); m&0o077 != 0 {
		t.Fatalf("lock file mode %04o", m)
	}
	_, err = AcquireLock(path)
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("second AcquireLock: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatal("Release must be idempotent")
	}
	l2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("after Release: %v", err)
	}
	_ = l2.Release()
	if _, err := AcquireLock(filepath.Join(t.TempDir(), "missing", "lock")); err == nil || errors.Is(err, ErrLocked) {
		t.Fatalf("a lock in a missing directory: %v", err)
	}
}

// TestLockHelperProcess holds a lock for TestLockSurvivesACrash.
func TestLockHelperProcess(t *testing.T) {
	path := os.Getenv("TARCIN_LOCK_HELPER")
	if path == "" {
		t.Skip("helper process only")
	}
	if _, err := AcquireLock(path); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Println("locked")
	time.Sleep(time.Minute)
	os.Exit(0)
}

func TestLockSurvivesACrash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLockHelperProcess$") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), "TARCIN_LOCK_HELPER="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(out).ReadString('\n')
	if strings.TrimSpace(line) != "locked" {
		_ = cmd.Process.Kill()
		t.Fatalf("helper: %q", line)
	}
	_, err = AcquireLock(path)
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), fmt.Sprintf("pid %d", cmd.Process.Pid)) {
		t.Fatalf("while another process holds the lock: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("the lock of a killed process must be free: %v", err)
	}
	_ = l.Release()
}
