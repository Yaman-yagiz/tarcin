// SPDX-License-Identifier: GPL-3.0-or-later

// Command tarcin is a terminal client for WhatsApp.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/Yaman-yagiz/tarcin/internal/app"
	"github.com/Yaman-yagiz/tarcin/internal/buildinfo"
	"github.com/Yaman-yagiz/tarcin/internal/diag"
	"github.com/Yaman-yagiz/tarcin/internal/paths"
)

// env is the program's environment, replaceable in tests.
type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	dumpKeys       func(ctx context.Context, in io.Reader, out io.Writer) error
	demo           func(ctx context.Context, in io.Reader, out io.Writer, o app.DemoOptions) error
	run            func(ctx context.Context, in io.Reader, out io.Writer, o app.RunOptions) error
	logout         func(ctx context.Context, in io.Reader, out io.Writer, o app.LogoutOptions) error
	backup         func(ctx context.Context, out io.Writer, o app.BackupOptions) error
	config         func(command, path string, out io.Writer) error
}

func main() {
	// Before any file is created: everything tarcin writes is private to
	// the user (docs/architecture/security.md §3).
	paths.SetUmask()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	code := run(ctx, os.Args[1:], env{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		dumpKeys: app.DumpKeys, demo: app.RunDemo, run: app.Run, logout: app.Logout, backup: app.Backup, config: app.Config,
	})
	stop()
	os.Exit(code)
}

// run parses the arguments and executes the program. It returns the
// process exit code: 0 on success, 1 on failure, 2 on usage errors.
func run(ctx context.Context, args []string, e env) int {
	fs := flag.NewFlagSet("tarcin", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	showVersion := fs.Bool("version", false, "print version information and exit")
	dumpKeys := fs.Bool("dump-keys", false, "show the names of the keys you press, for writing key bindings")
	demo := fs.Bool("demo", false, "run with simulated chats: no account or network needed")
	demoRate := fs.Float64("demo-rate", 2, "live messages per second in the demo")
	demoLink := fs.Bool("demo-link", false, "start the demo as a device that is not linked yet, to try the link screen")
	profile := fs.String("profile", paths.DefaultProfile, "the profile: one account with its own data")
	configPath := fs.String("config", "", "the config file (default $XDG_CONFIG_HOME/tarcin/config.toml)")
	pprofAddr := fs.String("pprof", "", "for developers: serve Go profiles on this loopback address, such as 127.0.0.1:6060")
	tracePath := fs.String("trace", "", "for developers: record an execution trace into this new file")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: tarcin [flags] [command]\n\n"+
			"Without a command, tarcin opens your chats, linking this device first if needed.\n\n"+
			"Commands:\n"+
			"  logout\tunlink this device from your account and delete its keys\n"+
			"  backup FILE\twrite your messages and settings to FILE, without the device's keys\n"+
			"  config check|path|print-defaults\n\tvalidate the config file, print its path, or print the defaults\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch fs.Arg(0) {
	case "":
	case "logout":
		return logout(ctx, fs.Args()[1:], *profile, e)
	case "backup":
		return backup(ctx, fs.Args()[1:], *profile, *configPath, e)
	case "config":
		return configCmd(fs.Args()[1:], *configPath, e)
	default:
		fmt.Fprintf(e.stderr, "tarcin: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}

	if *showVersion {
		fmt.Fprintln(e.stdout, buildinfo.Get())
		return 0
	}
	stopDiag, err := diagnostics(ctx, *pprofAddr, *tracePath, e.stderr)
	if err != nil {
		fmt.Fprintf(e.stderr, "tarcin: %v\n", err)
		return 2
	}
	defer stopDiag()
	switch {
	case *dumpKeys:
		return report(e, e.dumpKeys(ctx, e.stdin, e.stdout))
	case *demo:
		return report(e, e.demo(ctx, e.stdin, e.stdout, app.DemoOptions{Seed: 1, Rate: *demoRate, Link: *demoLink, ConfigPath: *configPath}))
	}
	return report(e, e.run(ctx, e.stdin, e.stdout, app.RunOptions{Profile: *profile, ConfigPath: *configPath, Warn: e.stderr}))
}

// diagnostics starts the profiles and the trace the flags ask for, and
// returns the function that stops them.
func diagnostics(ctx context.Context, pprofAddr, tracePath string, stderr io.Writer) (func(), error) {
	var stops []func()
	stop := func() {
		for _, s := range slices.Backward(stops) {
			s()
		}
	}
	if pprofAddr != "" {
		addr, s, err := diag.ServePprof(ctx, pprofAddr)
		if err != nil {
			return nil, err
		}
		stops = append(stops, s)
		fmt.Fprintf(stderr, "tarcin: profiles at http://%s/debug/pprof/\n", addr)
	}
	if tracePath != "" {
		s, err := diag.StartTrace(tracePath)
		if err != nil {
			stop()
			return nil, err
		}
		stops = append(stops, func() {
			if err := s(); err != nil {
				fmt.Fprintf(stderr, "tarcin: %v\n", err)
			}
		})
	}
	return stop, nil
}

// report prints err, if any, and returns the exit code.
func report(e env, err error) int {
	if err != nil {
		fmt.Fprintf(e.stderr, "tarcin: %v\n", err)
		return 1
	}
	return 0
}

// logout runs "tarcin logout [--profile name]".
func logout(ctx context.Context, args []string, profile string, e env) int {
	fs := flag.NewFlagSet("tarcin logout", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.StringVar(&profile, "profile", profile, "the profile to log out of")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: tarcin logout [flags]\n\n"+
			"Unlinks this device from your account, so that your phone no longer lists it,\n"+
			"and deletes its keys. You are then asked whether to delete your messages too.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "tarcin logout: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	return report(e, e.logout(ctx, e.stdin, e.stdout, app.LogoutOptions{Profile: profile}))
}

// backup runs "tarcin backup [--profile name] [--with-keys] FILE".
func backup(ctx context.Context, args []string, profile, config string, e env) int {
	fs := flag.NewFlagSet("tarcin backup", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.StringVar(&profile, "profile", profile, "the profile to back up")
	withKeys := fs.Bool("with-keys", false, "add the device's keys: anyone with the backup can then read your new messages")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: tarcin backup [flags] FILE\n\n"+
			"Writes your chats, messages, contacts and configuration to FILE, a new\n"+
			"gzipped tar archive, also while tarcin runs. Media and, unless asked\n"+
			"for, the device's keys are left out.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(e.stderr, "tarcin backup: name one file to write, after the flags")
		fs.Usage()
		return 2
	}
	return report(e, e.backup(ctx, e.stdout, app.BackupOptions{Profile: profile, Path: fs.Arg(0), WithKeys: *withKeys, ConfigPath: config}))
}

// configCmd runs "tarcin config <command>".
func configCmd(args []string, path string, e env) int {
	fs := flag.NewFlagSet("tarcin config", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.StringVar(&path, "config", path, "the config file")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: tarcin config check|path|print-defaults [flags]\n\n"+
			"  check           validate the config file\n"+
			"  path            print where the config file is\n"+
			"  print-defaults  print a complete, commented config with the defaults\n\nFlags:\n")
		fs.PrintDefaults()
	}
	switch {
	case len(args) == 0:
		fs.Usage()
		return 2
	case slices.Contains([]string{"-h", "-help", "--help"}, args[0]):
		fs.Usage()
		return 0
	case !slices.Contains(app.ConfigCommands, args[0]):
		fmt.Fprintf(e.stderr, "tarcin config: unknown command %q\n", args[0])
		fs.Usage()
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "tarcin config: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	return report(e, e.config(args[0], path, e.stdout))
}
