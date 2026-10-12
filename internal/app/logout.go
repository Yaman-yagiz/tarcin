// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/logging"
	"github.com/Yaman-yagiz/tarcin/internal/paths"
	"github.com/Yaman-yagiz/tarcin/internal/wa"
)

// logoutTimeout bounds connecting to WhatsApp to unlink the device.
const logoutTimeout = 30 * time.Second

// ErrLogoutCanceled means the device could not be unlinked and the user
// kept its keys.
var ErrLogoutCanceled = errors.New("logout canceled; this device is still linked")

// LogoutOptions configure Logout.
type LogoutOptions struct {
	Profile string // default: paths.DefaultProfile
	// linked and unlink report whether the device is linked and tell
	// WhatsApp to unlink it; tests replace them.
	linked func(s *wa.Session) bool
	unlink func(ctx context.Context, s *wa.Session) error
}

// Logout unlinks the profile's device from the account, so that the
// phone no longer lists it, and deletes its keys. It then asks whether
// to delete the messages and media on this computer too. Questions are
// read from in and answered no at the end of input.
func Logout(ctx context.Context, in io.Reader, out io.Writer, o LogoutOptions) error {
	if o.linked == nil {
		o.linked = (*wa.Session).Paired
	}
	if o.unlink == nil {
		o.unlink = func(ctx context.Context, s *wa.Session) error { return wa.New(s, wa.Options{}).Logout(ctx) }
	}
	p, err := paths.Default(cmp.Or(o.Profile, paths.DefaultProfile))
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.ProfileData()); errors.Is(err, fs.ErrNotExist) {
		_, _ = fmt.Fprintln(out, "Nothing to log out of: this profile has no data.")
		return nil
	}
	lock, err := paths.AcquireLock(p.LockFile())
	if errors.Is(err, paths.ErrLocked) {
		return fmt.Errorf("%w; quit it first", err)
	}
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	log := logging.Discard()
	if err := os.MkdirAll(p.State, 0o700); err == nil {
		if l, err := logging.Open(p.LogFile(), logging.Options{}); err == nil {
			defer func() { _ = l.Close() }()
			log = l.With("profile", p.Profile)
		}
	}
	answers := bufio.NewReader(in)
	if err := unlinkDevice(ctx, p, answers, out, &o, log); err != nil {
		log.Warn("logout not completed", "err", err)
		return err
	}
	if !exists(p.MessageDB()) && !exists(p.MediaCache()) {
		return nil
	}
	if !confirm(answers, out, "Also delete your messages and media from this computer? [y/N] ") {
		_, _ = fmt.Fprintf(out, "Your messages stay in %s.\n", p.ProfileData())
		return nil
	}
	if err := removeMessages(p); err != nil {
		return err
	}
	log.Info("messages and media deleted")
	_, _ = fmt.Fprintln(out, "Your messages and media are deleted.")
	return nil
}

// unlinkDevice unlinks the device and deletes the session store. If
// WhatsApp cannot be reached, it asks before deleting the keys: the phone
// would go on listing a device that no longer exists.
func unlinkDevice(ctx context.Context, p paths.Paths, answers *bufio.Reader, out io.Writer, o *LogoutOptions, log *slog.Logger) error {
	if !exists(p.SessionDB()) {
		_, _ = fmt.Fprintln(out, "No device is linked in this profile.")
		return nil
	}
	sess, err := openSession(ctx, p, log)
	if err != nil {
		return err
	}
	paired := o.linked(sess)
	if paired {
		_, _ = fmt.Fprintln(out, "Unlinking this device from your account…")
		uctx, cancel := context.WithTimeout(ctx, logoutTimeout)
		err := o.unlink(uctx, sess)
		cancel()
		if err != nil {
			log.Warn("unlinking the device on the server", "err", err)
			_, _ = fmt.Fprintf(out, "WhatsApp could not be told: %v\n", err)
			if !confirm(answers, out, "Delete this device's keys anyway? Then remove it on your phone under Linked devices. [y/N] ") {
				_ = sess.Close()
				return ErrLogoutCanceled
			}
		}
	}
	if err := sess.Close(); err != nil {
		return fmt.Errorf("closing the session store: %w", err)
	}
	if err := removeDB(p.SessionDB()); err != nil {
		return err
	}
	log.Info("logged out", "was_linked", paired)
	if paired {
		_, _ = fmt.Fprintln(out, "This device is unlinked and its keys are deleted.")
	} else {
		_, _ = fmt.Fprintln(out, "No device was linked; its unused keys are deleted.")
	}
	return nil
}

// removeMessages deletes the message database, its pre-migration
// snapshots and the media cache.
func removeMessages(p paths.Paths) error {
	if err := removeDB(p.MessageDB()); err != nil {
		return err
	}
	for _, dir := range []string{p.ProfileState(), filepath.Dir(p.MediaCache())} {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("deleting %s: %w", dir, err)
		}
	}
	return nil
}

// removeDB deletes a SQLite database and its journal files.
func removeDB(path string) error {
	for _, f := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("deleting %s: %w", f, err)
		}
	}
	return nil
}

// confirm asks a yes/no question; anything but yes is no.
func confirm(answers *bufio.Reader, out io.Writer, question string) bool {
	_, _ = fmt.Fprint(out, question)
	line, err := answers.ReadString('\n')
	if err != nil && line == "" {
		_, _ = fmt.Fprintln(out)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
