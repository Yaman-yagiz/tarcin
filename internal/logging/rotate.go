// SPDX-License-Identifier: GPL-3.0-or-later

package logging

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
)

// rotator is a log file that, once it would grow past max bytes, is
// renamed to path.1 (replacing the previous one) and started anew. Files
// are created with mode 0600.
type rotator struct {
	path string
	max  int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

func openRotator(path string, maxSize int64) (*rotator, error) {
	r := &rotator{path: path, max: maxSize}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotator) open() error {
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening the log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("opening the log: %w", err)
	}
	r.f, r.size = f, info.Size()
	return nil
}

// Write writes one log line. A line never spans two files.
func (r *rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, fs.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotator) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	if err := os.Rename(r.path, r.path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("rotating the log: %w", err)
	}
	return r.open()
}

func (r *rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
