// SPDX-License-Identifier: GPL-3.0-or-later

package paths

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrLocked means another process holds the profile's lock.
var ErrLocked = errors.New("another tarcin is already running with this profile")

// Lock is a held single-instance lock. Two processes on one session
// would corrupt the Signal state and look suspicious to the server
// (docs/architecture/security.md §3).
//
// The lock is an flock(2) on the lock file. The kernel releases it when
// the process exits, even after a crash or kill -9, so a stale lock file
// never blocks a later start. The file holds the holder's PID, for the
// error message only.
type Lock struct {
	f *os.File
}

// AcquireLock takes the lock at path without waiting. If another process
// holds it, the error wraps ErrLocked and names that process's PID when
// known.
func AcquireLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // the profile's lock path
	if err != nil {
		return nil, fmt.Errorf("opening the lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readPID(f)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if holder > 0 {
				return nil, fmt.Errorf("%w (pid %d)", ErrLocked, holder)
			}
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f}, nil
}

func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		return 0
	}
	return pid
}

// Release gives the lock up. The file stays: removing it would let a
// process that opened it before the removal lock a file nobody else sees.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = l.f.Truncate(0)
	err := l.f.Close() // closing the descriptor releases the flock
	l.f = nil
	return err
}
