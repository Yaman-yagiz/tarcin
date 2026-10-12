// SPDX-License-Identifier: GPL-3.0-or-later

// Package diag starts the diagnostics a developer asks for: Go's profiles
// over HTTP on a loopback address, and an execution trace into a file
// (docs/architecture/performance.md §5). Neither runs unless asked for,
// and neither holds message content.
package diag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"runtime/trace"
	"time"
)

// ServePprof serves net/http/pprof on addr, which must be a loopback
// address with a port, such as 127.0.0.1:6060. It returns the address it
// listens on and a function that stops it.
func ServePprof(ctx context.Context, addr string) (string, func(), error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", nil, fmt.Errorf("--pprof: %w", err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", nil, fmt.Errorf("--pprof: %q is not a loopback address; profiles never leave this computer", host)
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return "", nil, fmt.Errorf("--pprof: %w", err)
	}
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		_ = ln.Close()
		return "", nil, fmt.Errorf("--pprof: %s listens on %s, not a loopback address", host, ln.Addr())
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), func() { _ = srv.Close() }, nil
}

// StartTrace records an execution trace into a new file at path, private
// to the user, until the returned function stops it. It never replaces a
// file.
func StartTrace(path string) (func() error, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the developer's own --trace path
	if err != nil {
		return nil, fmt.Errorf("--trace: %w", err)
	}
	if err := trace.Start(f); err != nil {
		return nil, errors.Join(fmt.Errorf("--trace: %w", err), f.Close(), os.Remove(path))
	}
	return func() error {
		trace.Stop()
		return f.Close()
	}, nil
}
