// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// DefaultDeviceName is the name the phone shows under Linked devices.
const DefaultDeviceName = "tarcin"

// SetDeviceName sets the name and version the phone shows for this
// device. It must be called before pairing; the phone keeps the name it
// saw then. Without it, the phone would show "whatsmeow".
func SetDeviceName(name string, version [3]uint32) {
	store.SetOSInfo(name, version)
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
}

// Session is the device store in session.db: the device identity, its
// keys and its Signal sessions.
type Session struct {
	container *sqlstore.Container
	device    *store.Device
	log       *slog.Logger
}

// OpenSession opens the device store on db, which must be a writer
// connection of the store's SQLite driver: foreign keys on, one
// connection with BEGIN IMMEDIATE. whatsmeow's own migrations run here
// (they are whatsmeow's, never ours). The first device is used; a fresh
// store holds an unpaired one. The store, and adapters for the session
// that are given no logger of their own, log to log; nil logs nothing.
func OpenSession(ctx context.Context, db *sql.DB, log *slog.Logger) (*Session, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := sqlstore.NewWithDB(db, "sqlite3", newWALogger(log, "whatsmeow/store"))
	if err := c.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("upgrading the session store: %w", err)
	}
	dev, err := c.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading the device: %w", err)
	}
	return &Session{container: c, device: dev, log: log}, nil
}

// Paired reports whether the device is linked to an account.
func (s *Session) Paired() bool { return s.device.ID != nil }

// Account returns this account's phone-number JID and LID, or empty JIDs
// before pairing.
func (s *Session) Account() (pn, lid domain.JID) {
	if s.device.ID != nil {
		pn = fromJID(*s.device.ID)
	}
	return pn, fromJID(s.device.LID)
}

// Close closes the device store and its database.
func (s *Session) Close() error { return s.container.Close() }
