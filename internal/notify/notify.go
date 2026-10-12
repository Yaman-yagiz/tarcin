// SPDX-License-Identifier: GPL-3.0-or-later

// Package notify shows desktop notifications through
// org.freedesktop.Notifications on the session bus
// (docs/architecture/packaging.md §4): one notification per chat, which a
// newer message replaces; every call is bounded, since a notification
// service that advertises itself but does not answer would otherwise
// hang the caller.
package notify

import (
	"context"
	"errors"
	"fmt"
	"html"
	"slices"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Timeout bounds every call to the notification service.
const Timeout = 2 * time.Second

const (
	service = "org.freedesktop.Notifications"
	path    = "/org/freedesktop/Notifications"
)

// Notifier shows notifications. Create it with Connect.
type Notifier struct {
	conn   *dbus.Conn
	obj    dbus.BusObject
	markup bool // the server renders markup in bodies: text is escaped

	mu  sync.Mutex
	ids map[string]uint32 // the notification shown per key, to replace
}

// Connect opens the session bus and asks the notification service what
// it can do. It fails without a session bus (a TTY, SSH) or a service.
func Connect(ctx context.Context) (*Notifier, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("connecting to the session bus: %w", err)
	}
	n := &Notifier{conn: conn, obj: conn.Object(service, path), ids: map[string]uint32{}}
	var caps []string
	if err := n.obj.CallWithContext(ctx, service+".GetCapabilities", 0).Store(&caps); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("asking the notification service: %w", err)
	}
	n.markup = slices.Contains(caps, "body-markup")
	return n, nil
}

// Notify shows a notification, replacing the last one shown with the same
// key. summary and body must be plain, sanitized text.
func (n *Notifier) Notify(ctx context.Context, key, summary, body string) error {
	if n == nil {
		return errors.New("notify: not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if n.markup {
		body = html.EscapeString(body)
	}
	n.mu.Lock()
	replaces := n.ids[key]
	n.mu.Unlock()
	hints := map[string]dbus.Variant{
		"category":      dbus.MakeVariant("im.received"),
		"desktop-entry": dbus.MakeVariant("tarcin"),
	}
	var id uint32
	err := n.obj.CallWithContext(ctx, service+".Notify", 0,
		"tarcin", replaces, "", summary, body, []string{}, hints, int32(-1)).Store(&id)
	if err != nil {
		return fmt.Errorf("showing a notification: %w", err)
	}
	n.mu.Lock()
	n.ids[key] = id
	n.mu.Unlock()
	return nil
}

// Close closes the bus connection.
func (n *Notifier) Close() error {
	if n == nil {
		return nil
	}
	return n.conn.Close()
}
