// SPDX-License-Identifier: GPL-3.0-or-later

package diag

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServePprofOnlyOnLoopback(t *testing.T) {
	for _, addr := range []string{":6060", "0.0.0.0:6060", "192.168.1.5:6060", "example.com:6060", "127.0.0.1"} {
		if _, stop, err := ServePprof(t.Context(), addr); err == nil {
			stop()
			t.Errorf("ServePprof(%q) listens; only loopback addresses with a port may", addr)
		}
	}
	addr, stop, err := ServePprof(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/debug/pprof/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "goroutine") {
		t.Fatalf("GET /debug/pprof/: %s", resp.Status)
	}
}

func TestStartTrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tarcin.trace")
	stop, err := StartTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StartTrace(path); err == nil {
		t.Fatal("a second trace into the same file must fail")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("trace file: %v, %v", info, err)
	}
}
