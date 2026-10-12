// SPDX-License-Identifier: GPL-3.0-or-later

package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// SetUmask sets the process umask to 0077, so that every file and
// directory tarcin creates is private to the user, and returns the
// previous mask. It must run before any file is created.
func SetUmask() int { return syscall.Umask(0o077) }

// Problem is a private path that other users could reach.
type Problem struct {
	Path    string
	Mode    fs.FileMode // the current permission bits
	Want    fs.FileMode // the permission bits tarcin expects
	Owner   int         // the owning user ID
	Foreign bool        // owned by another user: tarcin cannot fix it
}

func (p Problem) String() string {
	if p.Foreign {
		return fmt.Sprintf("%s is owned by user %d, not by you", p.Path, p.Owner)
	}
	return fmt.Sprintf("%s has mode %04o; it should be %04o", p.Path, p.Mode, p.Want)
}

// Check returns the private directories and files that exist with
// permissions for group or others, or that belong to another user.
// Missing paths are not problems.
func (p Paths) Check() ([]Problem, error) {
	var out []Problem
	check := func(path string, want fs.FileMode) error {
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		pr := Problem{Path: path, Mode: info.Mode().Perm(), Want: want, Owner: -1}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			pr.Owner = int(st.Uid)
			pr.Foreign = pr.Owner != os.Getuid()
		}
		if pr.Foreign || pr.Mode&0o077 != 0 {
			out = append(out, pr)
		}
		return nil
	}
	for _, d := range p.privateDirs() {
		if err := check(d, 0o700); err != nil {
			return nil, err
		}
	}
	for _, f := range p.privateFiles() {
		if err := check(f, 0o600); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Fix tightens the modes of the problems it can fix: those of paths the
// user owns. It returns the problems left.
func Fix(problems []Problem) ([]Problem, error) {
	var left []Problem
	for _, pr := range problems {
		if pr.Foreign {
			left = append(left, pr)
			continue
		}
		if err := os.Chmod(pr.Path, pr.Want); err != nil {
			return nil, fmt.Errorf("fixing %s: %w", pr.Path, err)
		}
	}
	return left, nil
}
