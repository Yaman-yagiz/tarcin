// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDSNEscapesPaths(t *testing.T) {
	for _, dir := range []string{"plain", "with space", "q?mark", "hash#tag", "per%cent", "amp&er=x", "Türkçe ışık"} {
		t.Run(dir, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), dir)
			if err := os.MkdirAll(base, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(base, "tarcin.db")
			db, err := Open(context.Background(), path, RoleWriter)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(t.Context(), "CREATE TABLE t (x INTEGER)"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("database not at the requested path: %v", err)
			}
			entries, _ := os.ReadDir(filepath.Dir(base))
			if len(entries) != 1 {
				t.Fatalf("stray files next to the directory: %v", entries)
			}
		})
	}
	dsn, err := DSN("/a/b?c#d%e/x.db", RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(dsn, "?") != 1 || strings.Contains(dsn, "#") {
		t.Fatalf("DSN %q does not escape the path", dsn)
	}
	for _, role := range []Role{RoleWriter, RoleReader} {
		dsn, _ := DSN("/x.db", role)
		if !strings.Contains(dsn, "_stmt_cache_size=") {
			t.Errorf("role %d: DSN %q does not cache statements", role, dsn)
		}
	}
}

func TestPragmasAndRoles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tarcin.db")
	w, err := Open(ctx, path, RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r, err := Open(ctx, path, RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	for _, db := range []struct {
		name string
		q    func(string) string
	}{
		{"writer", func(p string) string {
			var v string
			_ = w.QueryRowContext(t.Context(), "PRAGMA "+p).Scan(&v)
			return v
		}},
		{"reader", func(p string) string {
			var v string
			_ = r.QueryRowContext(t.Context(), "PRAGMA "+p).Scan(&v)
			return v
		}},
	} {
		want := map[string]string{
			"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000",
			"synchronous": "1", "secure_delete": "1", "temp_store": "2",
		}
		for p, v := range want {
			if got := db.q(p); got != v {
				t.Errorf("%s: PRAGMA %s = %q, want %q", db.name, p, got, v)
			}
		}
	}
	if _, err := w.ExecContext(t.Context(), "CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ExecContext(t.Context(), "INSERT INTO t VALUES (1)"); err == nil {
		t.Fatal("a reader connection must not write")
	}
	if stats := w.Stats(); stats.MaxOpenConnections != 1 {
		t.Fatalf("writer pool = %d connections, want 1", stats.MaxOpenConnections)
	}
}

func TestFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tarcin.db")
	db, err := Open(context.Background(), path, RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE t (x INTEGER); INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s has mode %v, want 0600", filepath.Base(p), mode)
		}
	}
}

func TestFTS5Available(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "x.db"), RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := CheckFTS5(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(Version(), "3.") {
		t.Fatalf("unexpected SQLite version %q", Version())
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing", "x.db"), RoleWriter); err == nil {
		t.Fatal("opening in a missing directory must fail")
	}
}
