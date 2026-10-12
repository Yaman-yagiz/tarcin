// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Yaman-yagiz/tarcin/internal/app"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"--version"}, wantCode: 0, wantStdout: "tarcin "},
		{name: "single dash version", args: []string{"-version"}, wantCode: 0, wantStdout: "tarcin "},
		{name: "help", args: []string{"--help"}, wantCode: 0, wantStderr: "Usage: tarcin"},
		{name: "unknown flag", args: []string{"--nope"}, wantCode: 2, wantStderr: "flag provided but not defined"},
		{name: "unexpected argument", args: []string{"extra"}, wantCode: 2, wantStderr: `unexpected argument "extra"`},
		{name: "logout help", args: []string{"logout", "--help"}, wantCode: 0, wantStderr: "Usage: tarcin logout"},
		{name: "logout argument", args: []string{"logout", "now"}, wantCode: 2, wantStderr: `unexpected argument "now"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tt.args, env{stdout: &stdout, stderr: &stderr})
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestDumpKeys(t *testing.T) {
	var called bool
	var stderr bytes.Buffer
	e := env{stdout: io.Discard, stderr: &stderr, dumpKeys: func(context.Context, io.Reader, io.Writer) error {
		called = true
		return nil
	}}
	if code := run(context.Background(), []string{"--dump-keys"}, e); code != 0 || !called {
		t.Fatalf("--dump-keys: code %d, called %v", code, called)
	}
	e.dumpKeys = func(context.Context, io.Reader, io.Writer) error { return errors.New("no tty") }
	if code := run(context.Background(), []string{"--dump-keys"}, e); code != 1 || !strings.Contains(stderr.String(), "no tty") {
		t.Fatalf("failing --dump-keys: code %d, stderr %q", code, stderr.String())
	}
}

func TestDemo(t *testing.T) {
	var got app.DemoOptions
	e := env{stdout: io.Discard, stderr: io.Discard, demo: func(_ context.Context, _ io.Reader, _ io.Writer, o app.DemoOptions) error {
		got = o
		return nil
	}}
	if code := run(context.Background(), []string{"--demo", "--demo-rate", "25"}, e); code != 0 || got.Rate != 25 {
		t.Fatalf("--demo: code %d, options %+v", code, got)
	}
	e.demo = func(context.Context, io.Reader, io.Writer, app.DemoOptions) error { return errors.New("boom") }
	if code := run(context.Background(), []string{"--demo"}, e); code != 1 {
		t.Fatalf("a failing demo must exit 1, got %d", code)
	}
}

func TestRealMode(t *testing.T) {
	var got app.RunOptions
	var stderr bytes.Buffer
	e := env{stdout: io.Discard, stderr: &stderr, run: func(_ context.Context, _ io.Reader, _ io.Writer, o app.RunOptions) error {
		got = o
		return nil
	}}
	if code := run(context.Background(), nil, e); code != 0 || got.Profile != "default" || got.Warn != &stderr {
		t.Fatalf("no arguments: code %d, options %+v", code, got)
	}
	if code := run(context.Background(), []string{"--profile", "work"}, e); code != 0 || got.Profile != "work" {
		t.Fatalf("--profile: code %d, options %+v", code, got)
	}
	e.run = func(context.Context, io.Reader, io.Writer, app.RunOptions) error { return errors.New("locked") }
	if code := run(context.Background(), nil, e); code != 1 || !strings.Contains(stderr.String(), "tarcin: locked") {
		t.Fatalf("a failing run: code %d, stderr %q", code, stderr.String())
	}
}

func TestLogout(t *testing.T) {
	var got []string
	e := env{stdout: io.Discard, stderr: io.Discard, logout: func(_ context.Context, _ io.Reader, _ io.Writer, o app.LogoutOptions) error {
		got = append(got, o.Profile)
		return nil
	}}
	for _, args := range [][]string{{"logout"}, {"--profile", "work", "logout"}, {"logout", "--profile", "home"}} {
		if code := run(context.Background(), args, e); code != 0 {
			t.Fatalf("%q: code %d", args, code)
		}
	}
	if want := []string{"default", "work", "home"}; !slices.Equal(got, want) {
		t.Fatalf("profiles = %q, want %q", got, want)
	}
	e.logout = func(context.Context, io.Reader, io.Writer, app.LogoutOptions) error { return app.ErrLogoutCanceled }
	if code := run(context.Background(), []string{"logout"}, e); code != 1 {
		t.Fatalf("a canceled logout must exit 1, got %d", code)
	}
}

func TestConfigCommand(t *testing.T) {
	type call struct{ command, path string }
	var got []call
	e := env{stdout: io.Discard, stderr: io.Discard, config: func(command, path string, _ io.Writer) error {
		got = append(got, call{command, path})
		return nil
	}}
	for args, code := range map[string]int{
		"config check": 0, "config path --config /x.toml": 0, "--config /y.toml config print-defaults": 0,
		"config": 2, "config nope": 2, "config check extra": 2, "config --help": 0, "config check --help": 0,
	} {
		if c := run(context.Background(), strings.Fields(args), e); c != code {
			t.Errorf("%q: exit %d, want %d", args, c, code)
		}
	}
	slices.SortFunc(got, func(a, b call) int { return strings.Compare(a.command, b.command) })
	if want := []call{{"check", ""}, {"path", "/x.toml"}, {"print-defaults", "/y.toml"}}; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	e.config = func(string, string, io.Writer) error { return errors.New("config.toml:3: ui.font: unknown key") }
	var stderr bytes.Buffer
	e.stderr = &stderr
	if c := run(context.Background(), []string{"config", "check"}, e); c != 1 || !strings.Contains(stderr.String(), "ui.font") {
		t.Fatalf("a bad config: exit %d, stderr %q", c, stderr.String())
	}
	var o app.RunOptions
	e.run = func(_ context.Context, _ io.Reader, _ io.Writer, ro app.RunOptions) error { o = ro; return nil }
	if c := run(context.Background(), []string{"--config", "/z.toml"}, e); c != 0 || o.ConfigPath != "/z.toml" {
		t.Fatalf("--config: exit %d, options %+v", c, o)
	}
}

func TestDiagnosticsFlags(t *testing.T) {
	var stderr bytes.Buffer
	ran := false
	e := env{stdout: io.Discard, stderr: &stderr, dumpKeys: func(context.Context, io.Reader, io.Writer) error {
		ran = true
		return nil
	}}
	if code := run(context.Background(), []string{"--pprof", "0.0.0.0:6060", "--dump-keys"}, e); code != 2 || ran ||
		!strings.Contains(stderr.String(), "not a loopback address") {
		t.Fatalf("code %d, ran %v, stderr %q; profiles are served on loopback only", code, ran, stderr.String())
	}
	stderr.Reset()
	trace := filepath.Join(t.TempDir(), "run.trace")
	if code := run(context.Background(), []string{"--pprof", "127.0.0.1:0", "--trace", trace, "--dump-keys"}, e); code != 0 || !ran {
		t.Fatalf("code %d, ran %v, stderr %q", code, ran, stderr.String())
	}
	if !strings.Contains(stderr.String(), "/debug/pprof/") {
		t.Fatalf("stderr %q; tarcin says where the profiles are", stderr.String())
	}
	if info, err := os.Stat(trace); err != nil || info.Size() == 0 {
		t.Fatalf("trace: %v, %v", info, err)
	}
}

// TestDocsListEveryFlag keeps the man page and the shell completions in
// step with the flags.
func TestDocsListEveryFlag(t *testing.T) {
	var usage bytes.Buffer
	if code := run(context.Background(), []string{"--help"}, env{stdout: io.Discard, stderr: &usage}); code != 0 {
		t.Fatalf("--help: %d", code)
	}
	var flags []string
	for line := range strings.Lines(usage.String()) {
		if name, ok := strings.CutPrefix(line, "  -"); ok {
			flags = append(flags, strings.Fields(name)[0])
		}
	}
	if len(flags) < 9 {
		t.Fatalf("found %d flags in the usage", len(flags))
	}
	read := func(path string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", path)) //nolint:gosec // the repository's own files
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	man, bash, zsh, fish := read("docs/man/tarcin.1.scd"), read("packaging/completions/tarcin.bash"),
		read("packaging/completions/_tarcin"), read("packaging/completions/tarcin.fish")
	for _, f := range flags {
		for _, doc := range []struct {
			name, text string
			want       []string // any of them, so that --demo is not found in --demo-rate
		}{
			{"tarcin.1.scd", man, []string{"*--" + f + "*"}},
			{"tarcin.bash", bash, []string{"--" + f + " ", "--" + f + "'"}},
			{"_tarcin", zsh, []string{"'--" + f + "=", "'--" + f + "["}},
			{"tarcin.fish", fish, []string{"-l " + f + " "}},
		} {
			if !slices.ContainsFunc(doc.want, func(w string) bool { return strings.Contains(doc.text, w) }) {
				t.Errorf("%s does not list --%s", doc.name, f)
			}
		}
	}
	for _, cmd := range []string{"logout", "backup", "with-keys", "config", "check", "path", "print-defaults"} {
		for name, text := range map[string]string{"tarcin.1.scd": man, "tarcin.bash": bash, "_tarcin": zsh, "tarcin.fish": fish} {
			if !strings.Contains(text, cmd) {
				t.Errorf("%s does not list %s", name, cmd)
			}
		}
	}
}

func TestBackupCommand(t *testing.T) {
	var got app.BackupOptions
	var stderr bytes.Buffer
	e := env{stdout: io.Discard, stderr: &stderr, backup: func(_ context.Context, _ io.Writer, o app.BackupOptions) error {
		got = o
		return nil
	}}
	if code := run(context.Background(), []string{"--profile", "work", "backup", "--with-keys", "/tmp/b.tar.gz"}, e); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	if got.Profile != "work" || !got.WithKeys || got.Path != "/tmp/b.tar.gz" {
		t.Fatalf("options %+v", got)
	}
	if code := run(context.Background(), []string{"backup"}, e); code != 2 || !strings.Contains(stderr.String(), "name one file") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}
