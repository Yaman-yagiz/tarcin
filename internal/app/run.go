// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/buildinfo"
	"github.com/Yaman-yagiz/tarcin/internal/logging"
	"github.com/Yaman-yagiz/tarcin/internal/paths"
	"github.com/Yaman-yagiz/tarcin/internal/store"
	"github.com/Yaman-yagiz/tarcin/internal/wa"
)

// RunOptions configure a session with a WhatsApp account.
type RunOptions struct {
	Profile string // default: paths.DefaultProfile
	// ConfigPath is the config file (default: the XDG config file).
	ConfigPath string
	// Warn receives what is found before the UI starts, such as files
	// other users could read (default: discarded).
	Warn io.Writer
	// Extra program options, used by tests.
	ProgramOptions []tea.ProgramOption
}

// Run runs tarcin with the profile's account until the user quits. A
// device that is not linked yet opens on the first-run notice and links
// once the user accepts it.
func Run(ctx context.Context, in io.Reader, out io.Writer, o RunOptions) error {
	if o.Warn == nil {
		o.Warn = io.Discard
	}
	p, err := paths.Default(cmp.Or(o.Profile, paths.DefaultProfile))
	if err != nil {
		return err
	}
	set, err := loadSettings(o.ConfigPath)
	if err != nil {
		return err
	}
	lock, err := openProfile(p, o.Warn)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	log, err := logging.Open(p.LogFile(), logging.Options{Level: set.cfg.LogLevel()})
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	plog := log.With("profile", p.Profile)
	if n, err := removeLeftovers(p); err != nil || n > 0 {
		plog.Info("removed files a crash left behind", "files", n, "err", err)
	}
	set.drafts = p.Drafts()
	version := buildinfo.Get().Version
	wa.SetDeviceName(wa.DefaultDeviceName, deviceVersion(version))
	sess, err := openSession(ctx, p, plog)
	if err != nil {
		plog.Error("opening the session store", "err", err)
		return err
	}
	defer func() { _ = sess.Close() }()
	adapter := wa.New(sess, wa.Options{Log: plog})
	notifyFn, closeNotify := notifier(ctx, set, plog)
	defer closeNotify()
	set.notify = notifyFn
	pn, lid := sess.Account()
	pipe, err := openPipeline(ctx, p.MessageDB(), pipelineOptions{
		MeLID: lid, MePN: pn, SnapshotDir: p.ProfileState(), Backend: adapter, Log: plog,
		ReadReceipts: set.cfg.Privacy.ReadReceipts, Typing: set.cfg.Privacy.Typing, Presence: set.cfg.Privacy.Presence,
		Media: mediaFromConfig(set, p.MediaCache()),
	})
	if err != nil {
		plog.Error("opening the message store", "err", err)
		return err
	}
	pending, err := pipe.reader.PendingHistory(ctx)
	if err != nil {
		_ = pipe.close(ctx)
		return fmt.Errorf("reading the queued history: %w", err)
	}
	adapter.Resume(pending)
	welcome := ""
	if !sess.Paired() {
		welcome = homeRelative(p.ProfileData())
	}
	plog.Info("tarcin started", "version", version, "linked", sess.Paired(), "queued_history", len(pending),
		"config", set.cfg.Path() != "", "read_receipts", set.cfg.Privacy.ReadReceipts)
	// The pipeline closes when the session stops, before the device
	// store above.
	err = newSession(adapter, pipe, set, welcome).run(ctx, in, out, o.ProgramOptions)
	switch {
	case errors.Is(err, ErrCrashed):
		plog.Error("tarcin stopped", "err", err)
		return fmt.Errorf("%w; the details are in %s", err, homeRelative(p.LogFile()))
	case err != nil:
		plog.Error("tarcin stopped", "err", err)
	default:
		plog.Info("tarcin stopped")
	}
	return err
}

// openProfile creates the profile's directories, tightens the modes of
// files and directories other users could reach, warns about them, and
// takes the profile's lock (docs/adr/0008-xdg-paths-and-file-permissions.md).
func openProfile(p paths.Paths, warn io.Writer) (*paths.Lock, error) {
	if err := p.Ensure(); err != nil {
		return nil, err
	}
	problems, err := p.Check()
	if err != nil {
		return nil, fmt.Errorf("checking permissions: %w", err)
	}
	left, err := paths.Fix(problems)
	if err != nil {
		return nil, err
	}
	for _, pr := range problems {
		// Before the log is open: the log file may be one of them.
		if slices.Contains(left, pr) {
			_, _ = fmt.Fprintf(warn, "tarcin: warning: %s; other users may read your messages and keys\n", pr)
		} else {
			_, _ = fmt.Fprintf(warn, "tarcin: %s; fixed\n", pr)
		}
	}
	lock, err := paths.AcquireLock(p.LockFile())
	if errors.Is(err, paths.ErrLocked) {
		return nil, fmt.Errorf("%w; quit it first", err)
	}
	return lock, err
}

// removeLeftovers deletes what a crash leaves behind: drafts handed to the
// editor, and partial downloads and uploads in the media cache. It runs
// under the profile's lock, so no other tarcin is using them.
func removeLeftovers(p paths.Paths) (int, error) {
	n := 0
	var errs []error
	remove := func(path string) {
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, err)
			return
		}
		n++
	}
	drafts, err := os.ReadDir(p.Drafts())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, e := range drafts {
		remove(filepath.Join(p.Drafts(), e.Name()))
	}
	cache, err := os.ReadDir(p.MediaCache())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, e := range cache {
		if !e.IsDir() && isMediaTemp(e.Name()) {
			remove(filepath.Join(p.MediaCache(), e.Name()))
		}
	}
	return n, errors.Join(errs...)
}

// openSession opens the profile's device store.
func openSession(ctx context.Context, p paths.Paths, log *slog.Logger) (*wa.Session, error) {
	db, err := store.Open(ctx, p.SessionDB(), store.RoleWriter)
	if err != nil {
		return nil, err
	}
	sess, err := wa.OpenSession(ctx, db, log)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return sess, nil
}

// homeRelative writes a path under the home directory as ~/….
func homeRelative(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return path
}

// deviceVersion converts a version such as "0.1.0" for the phone's list
// of linked devices; a development build shows as 0.0.0.
func deviceVersion(v string) [3]uint32 {
	var out [3]uint32
	core, _, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out
	}
	for i, s := range parts {
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return [3]uint32{}
		}
		out[i] = uint32(n)
	}
	return out
}
