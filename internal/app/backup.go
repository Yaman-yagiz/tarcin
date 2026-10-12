// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"archive/tar"
	"cmp"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/paths"
	"github.com/Yaman-yagiz/tarcin/internal/store"
)

// BackupOptions configure Backup.
type BackupOptions struct {
	Profile string // default: paths.DefaultProfile
	// Path is the archive to write. It must not exist.
	Path string
	// WithKeys adds the device's keys (session.db). Anyone with them can
	// read the account's new messages, so they are left out by default.
	WithKeys bool
	// ConfigPath is the config file to include (default: the XDG one).
	ConfigPath string
}

// backupReadme explains an archive's contents and how to restore it.
const backupReadme = `tarcin backup

tarcin.db    your chats, messages and contacts
session.db   the device's keys, if the backup was made with --with-keys
config.toml  your configuration, if you had one

To restore, quit tarcin and copy tarcin.db (and session.db) into
$XDG_DATA_HOME/tarcin/profiles/<profile>/ (usually
~/.local/share/tarcin/profiles/default/), replacing what is there, with
mode 0600. Without session.db, link the device again.

Anyone with session.db can read your new messages: keep this file where
only you can read it.
`

// Backup writes a gzipped tar archive of a profile: a consistent copy of
// its message database, made with VACUUM INTO while tarcin may run, the
// config file, and the device's keys only if asked for. Media are left
// out: the cache can be downloaded again. The archive is 0600 and never
// replaces a file.
func Backup(ctx context.Context, out io.Writer, o BackupOptions) (err error) {
	if o.Path == "" {
		return errors.New("name the backup file")
	}
	p, err := paths.Default(cmp.Or(o.Profile, paths.DefaultProfile))
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.MessageDB()); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("profile %q has no messages to back up", p.Profile)
	}
	if err := p.Ensure(); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(p.ProfileState(), "backup-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	type entry struct{ name, path string }
	entries := []entry{{"tarcin.db", filepath.Join(tmp, "tarcin.db")}}
	dbs := map[string]string{p.MessageDB(): entries[0].path}
	if o.WithKeys {
		entries = append(entries, entry{"session.db", filepath.Join(tmp, "session.db")})
		dbs[p.SessionDB()] = entries[1].path
	}
	for src, dst := range dbs {
		if err := snapshot(ctx, src, dst); err != nil {
			return err
		}
	}
	config := o.ConfigPath
	if config == "" {
		config = p.ConfigFile()
	}
	if _, err := os.Stat(config); err == nil {
		entries = append(entries, entry{"config.toml", config})
	}

	f, err := os.OpenFile(o.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating the backup: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(o.Path)
		}
	}()
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	now := time.Now()
	if err := addFile(tw, "tarcin-backup/README", strings.NewReader(backupReadme), int64(len(backupReadme)), now); err != nil {
		return err
	}
	for _, e := range entries {
		if err := addPath(tw, "tarcin-backup/"+e.name, e.path, now); err != nil {
			return err
		}
	}
	if err := errors.Join(tw.Close(), zw.Close(), f.Sync()); err != nil {
		return fmt.Errorf("writing the backup: %w", err)
	}
	_, _ = fmt.Fprintf(out, "Backed up profile %q to %s.\n", p.Profile, o.Path)
	if o.WithKeys {
		_, _ = fmt.Fprintln(out, "It holds the device's keys: anyone with it can read your new messages. Keep it where only you can read it.")
	} else {
		_, _ = fmt.Fprintln(out, "The device's keys are not in it; after a restore, link the device again (or back up --with-keys).")
	}
	return nil
}

// snapshot copies the database at src to dst with VACUUM INTO. It only
// reads src, inside one read transaction, so tarcin's writer may go on;
// SQLite refuses it on a query-only connection, hence the writer role.
func snapshot(ctx context.Context, src, dst string) error {
	db, err := store.Open(ctx, src, store.RoleWriter)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return store.Backup(ctx, db, dst)
}

// addPath streams a file into the archive.
func addPath(tw *tar.Writer, name, path string, at time.Time) error {
	f, err := os.Open(path) //nolint:gosec // a snapshot or the config file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return addFile(tw, name, f, info.Size(), at)
}

func addFile(tw *tar.Writer, name string, r io.Reader, size int64, at time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: at, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := io.Copy(tw, r)
	return err
}
