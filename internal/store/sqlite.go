// SPDX-License-Identifier: GPL-3.0-or-later

// Package store is tarcin's local storage: the SQLite driver setup shared
// with whatsmeow's session store, the message database schema and
// migrations, the single writer and the read queries
// (docs/architecture/data-model.md).
//
// It is the only package that imports the SQLite driver (enforced by
// internal/archtest). Builds need the sqlite_fts5 tag (bundled SQLite) or
// the libsqlite3 tag (the system SQLite, as packaged for Arch).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/Yaman-yagiz/tarcin/internal/textfold"
)

// DriverName is the database/sql driver tarcin registers. whatsmeow's
// session store uses it too (go.mau.fi/util/dbutil accepts any driver
// name starting with "sqlite").
const DriverName = "sqlite3-tarcin"

// busyTimeout is how long a statement waits for a lock.
const busyTimeout = "PRAGMA busy_timeout = 5000"

// pragmas apply to every connection (docs/architecture/data-model.md §5).
var pragmas = []string{
	busyTimeout,
	"PRAGMA foreign_keys = ON",
	"PRAGMA synchronous = NORMAL",
	"PRAGMA temp_store = MEMORY",
	"PRAGMA cache_size = -16384",
	"PRAGMA journal_size_limit = 67108864",
	"PRAGMA secure_delete = ON",
}

func init() {
	sql.Register(DriverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(c *sqlite3.SQLiteConn) error {
			for _, p := range pragmas {
				if _, err := c.Exec(p, nil); err != nil {
					return fmt.Errorf("%s: %w", p, err)
				}
			}
			// textfold(s) sorts names the way search matches them, with
			// Turkish letters among their Latin ones. Queries only: no
			// index or stored value may depend on it, since a new
			// textfold version can change its keys.
			if err := c.RegisterFunc("textfold", textfold.Fold, true); err != nil {
				return fmt.Errorf("registering textfold: %w", err)
			}
			return nil
		},
	})
}

// Role selects how a database handle is opened.
type Role uint8

// Roles.
const (
	// RoleWriter is the single read-write connection; transactions start with
	// BEGIN IMMEDIATE so that lock upgrades never fail without waiting.
	RoleWriter Role = iota
	// RoleReader is a pool of query-only connections.
	RoleReader
)

// readerPool is the number of concurrent reader connections.
const readerPool = 4

// DSN returns the data source name for a database file. The path is
// escaped as a file: URI, so characters such as '?', '#' and '%' in a
// directory name cannot inject URI parameters.
func DSN(path string, role Role) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("database path %q: %w", path, err)
	}
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	// Prepared statements are cached per connection: preparing the same
	// SQL again cost 13% of the CPU under load (make storagebench).
	switch role {
	case RoleWriter:
		q.Set("_txlock", "immediate")
		q.Set("_stmt_cache_size", "64") // the applier uses about 40 statements
	case RoleReader:
		q.Set("_query_only", "1")
		q.Set("_stmt_cache_size", "32")
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	return u.String(), nil
}

// Open opens a database file for role. The file is created with mode
// 0600 if it does not exist; SQLite gives the -wal and -shm files the
// same mode. The directory must exist.
func Open(ctx context.Context, path string, role Role) (*sql.DB, error) {
	if role == RoleWriter {
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // the caller's database path
		if err != nil {
			return nil, fmt.Errorf("creating database: %w", err)
		}
		_ = f.Close()
	}
	dsn, err := DSN(path, role)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", filepath.Base(path), err)
	}
	switch role {
	case RoleWriter:
		db.SetMaxOpenConns(1)
	case RoleReader:
		db.SetMaxOpenConns(readerPool)
	}
	db.SetMaxIdleConns(readerPool)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening %s: %w", filepath.Base(path), err)
	}
	return db, nil
}

// ErrNoFTS5 means the SQLite library lacks full-text search.
var ErrNoFTS5 = errors.New("this SQLite has no FTS5: build with -tags sqlite_fts5 (bundled SQLite) or -tags libsqlite3 (system SQLite)")

// CheckFTS5 verifies that the linked SQLite supports FTS5.
func CheckFTS5(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA compile_options")
	if err != nil {
		return fmt.Errorf("reading compile options: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var opt string
		if err := rows.Scan(&opt); err != nil {
			return err
		}
		if strings.EqualFold(opt, "ENABLE_FTS5") {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return ErrNoFTS5
}

// Version returns the SQLite library version, e.g. "3.53.4".
func Version() string {
	v, _, _ := sqlite3.Version()
	return v
}
