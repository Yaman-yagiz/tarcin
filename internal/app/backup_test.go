// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend/fake"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
	"github.com/Yaman-yagiz/tarcin/internal/paths"
	"github.com/Yaman-yagiz/tarcin/internal/store"
)

// readArchive returns a backup's files by name.
func readArchive(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // the test's archive
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Mode != 0o600 {
			t.Errorf("%s: mode %o", h.Name, h.Mode)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = data
	}
}

func TestBackup(t *testing.T) {
	p := testProfile(t)
	out := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := Backup(t.Context(), io.Discard, BackupOptions{Path: out}); err == nil || !strings.Contains(err.Error(), "no messages") {
		t.Fatalf("a profile without data: %v", err)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	sess, err := openSession(t.Context(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.Close()
	fb := fake.New(fake.Config{Seed: 4, Chats: 1, GroupRatio: 1, MembersPerGroup: 2})
	pipe, err := openPipeline(t.Context(), p.MessageDB(), pipelineOptions{MeLID: fb.Me().LID, Backend: fb})
	if err != nil {
		t.Fatal(err)
	}
	if err := pipe.start(newRecordingUI()); err != nil {
		t.Fatal(err)
	}
	g := fb.Chats()[0]
	if err := pipe.Deliver(t.Context(), []domain.Event{domain.MessageReceived{Source: domain.SourceLive, Message: domain.IncomingMessage{
		ChatJID: g.JID, SenderJID: g.Members[0].LID, WAID: "M1", Timestamp: time.Now(), Kind: domain.KindText, Body: "yedekle beni",
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), []byte("[ui]\ntheme = \"plain\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// tarcin runs: the pipeline is open and the profile is locked.
	lock, err := paths.AcquireLock(p.LockFile())
	if err != nil {
		t.Fatal(err)
	}
	var msg bytes.Buffer
	if err := Backup(t.Context(), &msg, BackupOptions{Path: out}); err != nil {
		t.Fatal(err)
	}
	_ = lock.Release()
	if err := pipe.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive: %v, %v", info, err)
	}
	files := readArchive(t, out)
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range files {
			if !yield(n) {
				return
			}
		}
	})
	if want := []string{"tarcin-backup/README", "tarcin-backup/config.toml", "tarcin-backup/tarcin.db"}; !slices.Equal(names, want) {
		t.Fatalf("archive holds %v, want %v: no keys by default", names, want)
	}
	if !strings.Contains(msg.String(), "keys are not in it") {
		t.Fatalf("message %q", msg.String())
	}
	copyDB := filepath.Join(t.TempDir(), "tarcin.db")
	if err := os.WriteFile(copyDB, files["tarcin-backup/tarcin.db"], 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), copyDB, store.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var body string
	if err := db.QueryRowContext(t.Context(), "SELECT body FROM messages WHERE wa_id = 'M1'").Scan(&body); err != nil || body != "yedekle beni" {
		t.Fatalf("the copy holds %q, %v", body, err)
	}

	if err := Backup(t.Context(), io.Discard, BackupOptions{Path: out}); err == nil {
		t.Fatal("a backup never replaces a file")
	}
	if again := readArchive(t, out); len(again) != len(files) {
		t.Fatal("the existing archive must stay as it was")
	}

	withKeys := filepath.Join(t.TempDir(), "keys.tar.gz")
	msg.Reset()
	if err := Backup(t.Context(), &msg, BackupOptions{Path: withKeys, WithKeys: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := readArchive(t, withKeys)["tarcin-backup/session.db"]; !ok || !strings.Contains(msg.String(), "anyone with it can read") {
		t.Fatalf("--with-keys: %q", msg.String())
	}
	if left, _ := os.ReadDir(p.ProfileState()); slices.ContainsFunc(left, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), "backup-") }) {
		t.Fatal("the snapshots must be removed")
	}
}
