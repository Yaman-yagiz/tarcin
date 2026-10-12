// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// ErrBackupExists is returned when the backup target is a non-empty file.
var ErrBackupExists = errors.New("backup target exists and is not empty")

// Backup writes a consistent, compacted copy of db to path with VACUUM
// INTO. Unlike copying the file, it includes the WAL and never captures a
// half-written transaction. The target is created with mode 0600 and
// must not exist yet (an existing empty file is accepted).
func Backup(ctx context.Context, db *sql.DB, path string) error {
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return fmt.Errorf("%w: %s", ErrBackupExists, path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the caller's backup path
	if err != nil {
		return fmt.Errorf("creating backup: %w", err)
	}
	_ = f.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("writing backup: %w", err)
	}
	return nil
}
