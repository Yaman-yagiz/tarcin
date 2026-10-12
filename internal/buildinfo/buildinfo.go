// SPDX-License-Identifier: GPL-3.0-or-later

// Package buildinfo reports the version of the running binary.
//
// Release builds inject the values at link time:
//
//	go build -ldflags "-linkmode=external \
//	  -X github.com/Yaman-yagiz/tarcin/internal/buildinfo.Version=0.1.0 \
//	  -X github.com/Yaman-yagiz/tarcin/internal/buildinfo.Commit=abc1234 \
//	  -X github.com/Yaman-yagiz/tarcin/internal/buildinfo.Date=2026-10-11T12:00:00Z"
//
// When a value is not injected, it falls back to the module and VCS
// metadata that the Go toolchain embeds (see runtime/debug.ReadBuildInfo).
// The build date is never taken from the wall clock, so that builds stay
// reproducible.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Values set with -ldflags -X. They must stay plain string variables.
var (
	Version string
	Commit  string
	Date    string
)

const unknown = "unknown"

// Info describes the build of the running binary.
type Info struct {
	Version   string // semantic version without a leading "v", or "devel"
	Commit    string // abbreviated VCS revision, or "unknown"
	Date      string // commit or source date in RFC 3339, or "unknown"
	Modified  bool   // true if built from a working tree with local changes
	GoVersion string // Go toolchain version, e.g. "go1.27.2"
}

// Get returns the build information of the running binary.
func Get() Info {
	bi, _ := debug.ReadBuildInfo()
	return resolve(Version, Commit, Date, bi)
}

// resolve merges injected values with embedded build metadata. Injected
// values take precedence. It is separate from Get so that tests can
// supply their own metadata.
func resolve(version, commit, date string, bi *debug.BuildInfo) Info {
	info := Info{
		Version:   strings.TrimPrefix(version, "v"),
		Commit:    commit,
		Date:      date,
		GoVersion: runtime.Version(),
	}

	if bi != nil {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			// The toolchain marks VCS-derived versions of modified trees
			// with "+dirty"; that state is reported through Modified.
			v := strings.TrimSuffix(bi.Main.Version, "+dirty")
			info.Version = strings.TrimPrefix(v, "v")
		}
		if bi.GoVersion != "" {
			info.GoVersion = bi.GoVersion
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = shortRevision(s.Value)
				}
			case "vcs.time":
				if info.Date == "" {
					info.Date = s.Value
				}
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}

	if info.Version == "" {
		info.Version = "devel"
	}
	if info.Commit == "" {
		info.Commit = unknown
	}
	if info.Date == "" {
		info.Date = unknown
	}
	return info
}

func shortRevision(rev string) string {
	const n = 12
	if len(rev) > n {
		return rev[:n]
	}
	return rev
}

// String formats the information on one line, for example
// "tarcin 0.1.0 (commit abc1234, 2026-10-11T12:00:00Z, go1.27.2)".
func (i Info) String() string {
	commit := i.Commit
	if i.Modified {
		commit += "-dirty"
	}
	return fmt.Sprintf("tarcin %s (commit %s, %s, %s)", i.Version, commit, i.Date, i.GoVersion)
}
