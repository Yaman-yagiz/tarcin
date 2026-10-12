// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func openWriter(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tarcin.db")
	db, err := Open(t.Context(), path, RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func version(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name = ?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

var testMigrations = fstest.MapFS{
	"0001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER);")},
	"0002_b.sql": {Data: []byte("CREATE TABLE b (y INTEGER); INSERT INTO a VALUES (1);")},
}

func TestMigrateFreshAndIdempotent(t *testing.T) {
	db := openWriter(t)
	from, to, err := Migrate(t.Context(), db, testMigrations, MigrateOptions{})
	if err != nil || from != 0 || to != 2 || version(t, db) != 2 {
		t.Fatalf("Migrate = %d→%d, %v; user_version %d", from, to, err, version(t, db))
	}
	if !tableExists(t, db, "a") || !tableExists(t, db, "b") {
		t.Fatal("tables missing")
	}
	from, to, err = Migrate(t.Context(), db, testMigrations, MigrateOptions{})
	if err != nil || from != 2 || to != 2 {
		t.Fatalf("re-running must be a no-op: %d→%d, %v", from, to, err)
	}
}

func TestMigrateSnapshotsBeforeUpgrading(t *testing.T) {
	db := openWriter(t)
	snapDir := filepath.Join(t.TempDir(), "state")
	if _, _, err := Migrate(t.Context(), db, testMigrations, MigrateOptions{SnapshotDir: snapDir}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(snapDir); len(entries) != 0 {
		t.Fatal("a fresh database needs no snapshot")
	}
	next := fstest.MapFS{"0003_c.sql": {Data: []byte("CREATE TABLE c (z INTEGER);")}}
	for k, v := range testMigrations {
		next[k] = v
	}
	if _, to, err := Migrate(t.Context(), db, next, MigrateOptions{SnapshotDir: snapDir}); err != nil || to != 3 {
		t.Fatalf("upgrade: to=%d err=%v", to, err)
	}
	snap := filepath.Join(snapDir, "tarcin.pre-migration-v2.db")
	info, err := os.Stat(snap)
	if err != nil {
		t.Fatalf("no snapshot: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode %v, want 0600", info.Mode().Perm())
	}
	s, err := Open(t.Context(), snap, RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	if version(t, s) != 2 || tableExists(t, s, "c") {
		t.Fatal("the snapshot must hold the database as it was before the upgrade")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// A further upgrade replaces the snapshot, sidecar files included.
	if err := os.WriteFile(snap+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	next["0004_d.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE d (w INTEGER);")}
	if _, _, err := Migrate(t.Context(), db, next, MigrateOptions{SnapshotDir: snapDir}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(snapDir)
	if len(entries) != 1 || entries[0].Name() != "tarcin.pre-migration-v3.db" {
		t.Fatalf("snapshots = %v, want only the newest", entries)
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	db := openWriter(t)
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = 9"); err != nil {
		t.Fatal(err)
	}
	_, _, err := Migrate(t.Context(), db, testMigrations, MigrateOptions{})
	if !errors.Is(err, ErrSchemaTooNew) || !strings.Contains(err.Error(), "v9") {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
	if version(t, db) != 9 || tableExists(t, db, "a") {
		t.Fatal("a refused migration must not touch the database")
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	db := openWriter(t)
	bad := fstest.MapFS{
		"0001_a.sql":   {Data: []byte("CREATE TABLE a (x INTEGER);")},
		"0002_bad.sql": {Data: []byte("CREATE TABLE b (y INTEGER); THIS IS NOT SQL;")},
	}
	from, to, err := Migrate(t.Context(), db, bad, MigrateOptions{})
	if err == nil || !strings.Contains(err.Error(), "0002_bad.sql") || from != 0 || to != 1 {
		t.Fatalf("Migrate = %d→%d, %v", from, to, err)
	}
	if version(t, db) != 1 || tableExists(t, db, "b") {
		t.Fatal("the failed migration must leave no trace and version 1")
	}
}

func TestMigrationFileRules(t *testing.T) {
	for name, src := range map[string]fstest.MapFS{
		"gap":       {"0001_a.sql": {Data: []byte("SELECT 1;")}, "0003_c.sql": {Data: []byte("SELECT 1;")}},
		"bad name":  {"1_a.sql": {Data: []byte("SELECT 1;")}},
		"uppercase": {"0001_A.sql": {Data: []byte("SELECT 1;")}},
		"empty":     {"0001_a.sql": {Data: []byte("  \n")}},
		"not first": {"0002_b.sql": {Data: []byte("SELECT 1;")}},
	} {
		if _, err := loadMigrations(src); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestBackup(t *testing.T) {
	db := openWriter(t)
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE t (x TEXT); INSERT INTO t VALUES ('kept')"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "backup.db")
	if err := Backup(t.Context(), db, dst); err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.Context(), dst, RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var x string
	if err := b.QueryRowContext(t.Context(), "SELECT x FROM t").Scan(&x); err != nil || x != "kept" {
		t.Fatalf("backup content: %q, %v", x, err)
	}
	if err := Backup(t.Context(), db, dst); !errors.Is(err, ErrBackupExists) {
		t.Fatalf("overwriting a backup: err = %v", err)
	}
	if err := Backup(context.Background(), db, filepath.Join(t.TempDir(), "no", "dir.db")); err == nil {
		t.Fatal("a backup into a missing directory must fail")
	}
}

func TestEmbeddedMigrationsApply(t *testing.T) {
	db := openWriter(t)
	_, to, err := Migrate(t.Context(), db, Migrations(), MigrateOptions{})
	if err != nil || to < 2 {
		t.Fatalf("embedded migrations: to=%d err=%v", to, err)
	}
}
