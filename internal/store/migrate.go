// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the embedded schema migrations.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	return sub
}

// ErrSchemaTooNew means the database was created by a newer tarcin.
// Downgrades are not supported, as in whatsmeow.
var ErrSchemaTooNew = errors.New("database schema is newer than this version of tarcin; downgrading is not supported")

type migration struct {
	version int
	name    string
	sql     string
}

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// loadMigrations reads NNNN_name.sql files, which must be numbered from
// 0001 without gaps.
func loadMigrations(src fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(src, ".")
	if err != nil {
		return nil, fmt.Errorf("reading migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil || e.IsDir() {
			return nil, fmt.Errorf("migration %q: want NNNN_name.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		data, err := fs.ReadFile(src, e.Name())
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, fmt.Errorf("migration %s is empty", e.Name())
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(data)})
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: expected version %04d (versions start at 0001 without gaps)", m.name, i+1)
		}
	}
	return out, nil
}

// MigrateOptions configure Migrate.
type MigrateOptions struct {
	// SnapshotDir, if set, receives a VACUUM INTO snapshot of a non-empty
	// database before it is migrated. Only the newest snapshot is kept.
	SnapshotDir string
	// SnapshotName is the base name of snapshot files (default "tarcin").
	SnapshotName string
}

// Migrate brings db to the newest schema in src. Each migration runs in
// its own transaction together with the PRAGMA user_version update, so a
// failing migration leaves the database at the previous version. It
// returns the versions before and after.
func Migrate(ctx context.Context, db *sql.DB, src fs.FS, opts MigrateOptions) (from, to int, err error) {
	ms, err := loadMigrations(src)
	if err != nil {
		return 0, 0, err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&from); err != nil {
		return 0, 0, fmt.Errorf("reading schema version: %w", err)
	}
	latest := len(ms)
	switch {
	case from > latest:
		return from, from, fmt.Errorf("%w (database v%d, tarcin knows v%d)", ErrSchemaTooNew, from, latest)
	case from == latest:
		return from, from, nil
	}
	if from > 0 && opts.SnapshotDir != "" {
		if err := snapshot(ctx, db, opts, from); err != nil {
			return from, from, err
		}
	}
	to = from
	for _, m := range ms[from:] {
		if err := apply(ctx, db, m); err != nil {
			return from, to, err
		}
		to = m.version
	}
	return from, to, nil
}

func apply(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	// PRAGMA does not take parameters; the version is an integer we parsed.
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(m.version)); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	return nil
}

// snapshot backs the database up before migrating from version v and
// removes older snapshots.
func snapshot(ctx context.Context, db *sql.DB, opts MigrateOptions, v int) error {
	name := opts.SnapshotName
	if name == "" {
		name = "tarcin"
	}
	if err := os.MkdirAll(opts.SnapshotDir, 0o700); err != nil {
		return fmt.Errorf("snapshot directory: %w", err)
	}
	pattern := filepath.Join(opts.SnapshotDir, name+".pre-migration-v*.db")
	old, _ := filepath.Glob(pattern)
	path := filepath.Join(opts.SnapshotDir, fmt.Sprintf("%s.pre-migration-v%d.db", name, v))
	removeDB(path) // a leftover from an interrupted earlier attempt
	if err := Backup(ctx, db, path); err != nil {
		return fmt.Errorf("snapshot before migrating: %w", err)
	}
	for _, p := range old {
		if p != path {
			removeDB(p)
		}
	}
	return nil
}

// removeDB removes a database file and the sidecar files SQLite may have
// left next to it.
func removeDB(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}
