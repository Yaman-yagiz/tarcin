// SPDX-License-Identifier: GPL-3.0-or-later

package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestResolveInjectedValuesWin(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.27.2",
		Main:      debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "ffffffffffffffffffff"},
			{Key: "vcs.time", Value: "2000-01-01T00:00:00Z"},
		},
	}
	got := resolve("v0.1.0", "abc1234", "2026-10-11T12:00:00Z", bi)
	want := Info{
		Version:   "0.1.0",
		Commit:    "abc1234",
		Date:      "2026-10-11T12:00:00Z",
		GoVersion: "go1.27.2",
	}
	if got != want {
		t.Fatalf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolveFallsBackToBuildInfo(t *testing.T) {
	bi := &debug.BuildInfo{
		GoVersion: "go1.27.2",
		Main:      debug.Module{Version: "v0.2.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123"},
			{Key: "vcs.time", Value: "2026-10-11T12:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	got := resolve("", "", "", bi)
	want := Info{
		Version:   "0.2.0",
		Commit:    "0123456789ab",
		Date:      "2026-10-11T12:00:00Z",
		Modified:  true,
		GoVersion: "go1.27.2",
	}
	if got != want {
		t.Fatalf("resolve() = %+v, want %+v", got, want)
	}
}

func TestResolveStripsDirtySuffix(t *testing.T) {
	bi := &debug.BuildInfo{
		Main:     debug.Module{Version: "v0.0.0-20261011131630-9d45c080b363+dirty"},
		Settings: []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}},
	}
	got := resolve("", "", "", bi)
	if got.Version != "0.0.0-20261011131630-9d45c080b363" || !got.Modified {
		t.Fatalf("resolve() = %+v, want version without +dirty and Modified=true", got)
	}
}

func TestResolveDevelBuild(t *testing.T) {
	bi := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	got := resolve("", "", "", bi)
	if got.Version != "devel" || got.Commit != unknown || got.Date != unknown {
		t.Fatalf("resolve() = %+v, want devel/unknown/unknown", got)
	}
	if got.GoVersion == "" {
		t.Fatal("GoVersion must fall back to runtime.Version()")
	}
}

func TestResolveWithoutBuildInfo(t *testing.T) {
	got := resolve("", "", "", nil)
	if got.Version != "devel" {
		t.Fatalf("Version = %q, want devel", got.Version)
	}
}

func TestString(t *testing.T) {
	i := Info{Version: "0.1.0", Commit: "abc1234", Date: "2026-10-11T12:00:00Z", Modified: true, GoVersion: "go1.27.2"}
	got := i.String()
	want := "tarcin 0.1.0 (commit abc1234-dirty, 2026-10-11T12:00:00Z, go1.27.2)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(Get().String(), "tarcin ") {
		t.Fatal("Get().String() must start with the program name")
	}
}
