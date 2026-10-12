// SPDX-License-Identifier: GPL-3.0-or-later

// Package paths resolves tarcin's directories by the XDG Base Directory
// specification and keeps them private (docs/adr/0008-xdg-paths-and-file-permissions.md):
// directories are 0700, files 0600, and a lock file keeps a second
// process off a profile.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// DefaultProfile is the profile used when none is chosen.
const DefaultProfile = "default"

// Paths are the directories of one profile. Data, State and Cache hold
// per-profile subdirectories; Config is shared by all profiles.
type Paths struct {
	Profile string
	Config  string // $XDG_CONFIG_HOME/tarcin: config.toml only, no secrets
	Data    string // $XDG_DATA_HOME/tarcin: databases and device keys
	State   string // $XDG_STATE_HOME/tarcin: logs, snapshots, UI state
	Cache   string // $XDG_CACHE_HOME/tarcin: media; always safe to delete
	// Runtime is $XDG_RUNTIME_DIR/tarcin, or empty when the variable is
	// unset or not absolute. Nothing is stored there yet.
	Runtime string
}

var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ErrBadProfile is returned for profile names that are not 1–32
// lowercase letters, digits, '-' or '_' (starting with a letter or
// digit), which also keeps them from naming another directory.
var ErrBadProfile = errors.New("invalid profile name")

// Resolve computes the paths of profile from an environment lookup and
// the home directory. XDG variables that are unset, empty or relative
// fall back to the spec's defaults under home. TARCIN_CONFIG_DIR,
// TARCIN_DATA_DIR, TARCIN_STATE_DIR and TARCIN_CACHE_DIR replace a whole
// tarcin directory, for tests and portable setups, and must be absolute.
func Resolve(getenv func(string) string, home, profile string) (Paths, error) {
	if !profileName.MatchString(profile) {
		return Paths{}, fmt.Errorf("%w: %q", ErrBadProfile, profile)
	}
	if !filepath.IsAbs(home) {
		return Paths{}, fmt.Errorf("home directory %q is not absolute", home)
	}
	p := Paths{Profile: profile}
	for _, d := range []struct {
		dst           *string
		override, xdg string
		fallback      []string
	}{
		{&p.Config, "TARCIN_CONFIG_DIR", "XDG_CONFIG_HOME", []string{".config"}},
		{&p.Data, "TARCIN_DATA_DIR", "XDG_DATA_HOME", []string{".local", "share"}},
		{&p.State, "TARCIN_STATE_DIR", "XDG_STATE_HOME", []string{".local", "state"}},
		{&p.Cache, "TARCIN_CACHE_DIR", "XDG_CACHE_HOME", []string{".cache"}},
	} {
		if v := getenv(d.override); v != "" {
			if !filepath.IsAbs(v) {
				return Paths{}, fmt.Errorf("%s must be an absolute path, not %q", d.override, v)
			}
			*d.dst = filepath.Clean(v)
			continue
		}
		base := getenv(d.xdg)
		if !filepath.IsAbs(base) {
			base = filepath.Join(append([]string{home}, d.fallback...)...)
		}
		*d.dst = filepath.Join(base, "tarcin")
	}
	if r := getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(r) {
		p.Runtime = filepath.Join(r, "tarcin")
	}
	return p, nil
}

// Default resolves profile's paths from the process environment.
func Default(profile string) (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("finding the home directory: %w", err)
	}
	return Resolve(os.Getenv, home, profile)
}

// ProfileData is the directory of the profile's databases.
func (p Paths) ProfileData() string { return filepath.Join(p.Data, "profiles", p.Profile) }

// SessionDB is whatsmeow's device store: the device keys and Signal
// sessions.
func (p Paths) SessionDB() string { return filepath.Join(p.ProfileData(), "session.db") }

// MessageDB is tarcin's database of chats, messages and people.
func (p Paths) MessageDB() string { return filepath.Join(p.ProfileData(), "tarcin.db") }

// LockFile is the profile's single-instance lock. It lives next to the
// databases it protects, not in the runtime directory, which belongs to
// one login session.
func (p Paths) LockFile() string { return filepath.Join(p.ProfileData(), "lock") }

// ProfileState holds the profile's pre-migration snapshots and UI state.
func (p Paths) ProfileState() string { return filepath.Join(p.State, "profiles", p.Profile) }

// LogFile is the log of all profiles; LogFile()+".1" holds the previous
// part once it has rotated.
func (p Paths) LogFile() string { return filepath.Join(p.State, "tarcin.log") }

// MediaCache holds the profile's downloaded media.
func (p Paths) MediaCache() string { return filepath.Join(p.Cache, "profiles", p.Profile, "media") }

// Drafts holds the profile's drafts while $EDITOR edits them. tarcin
// empties it at start, under the profile's lock.
func (p Paths) Drafts() string { return filepath.Join(p.ProfileState(), "drafts") }

// ConfigFile is the user's configuration, which tarcin never writes.
func (p Paths) ConfigFile() string { return filepath.Join(p.Config, "config.toml") }

// privateDirs are the directories tarcin creates, all mode 0700. The
// config directory is the user's and is never created.
func (p Paths) privateDirs() []string {
	return []string{
		p.Data, filepath.Join(p.Data, "profiles"), p.ProfileData(),
		p.State, filepath.Join(p.State, "profiles"), p.ProfileState(), p.Drafts(),
		p.Cache, filepath.Join(p.Cache, "profiles"), filepath.Dir(p.MediaCache()), p.MediaCache(),
	}
}

// privateFiles are the files that must be 0600 when they exist.
func (p Paths) privateFiles() []string {
	var files []string
	for _, db := range []string{p.SessionDB(), p.MessageDB()} {
		files = append(files, db, db+"-wal", db+"-shm")
	}
	return append(files, p.LockFile(), p.LogFile(), p.LogFile()+".1")
}

// Ensure creates the private directories, and missing parents such as
// ~/.local/share, with mode 0700 as the XDG spec asks. It does not change
// existing directories; Check reports those that are looser.
func (p Paths) Ensure() error {
	for _, d := range p.privateDirs() {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}
	return nil
}
