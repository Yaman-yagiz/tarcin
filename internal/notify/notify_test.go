// SPDX-License-Identifier: GPL-3.0-or-later

package notify

import (
	"context"
	"strings"
	"testing"
)

// The bus itself is exercised by the end-to-end checklist; here only what
// needs none.

func TestConnectWithoutABus(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/tarcin-test-bus")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	n, err := Connect(context.Background())
	if err == nil {
		_ = n.Close()
		t.Fatal("no bus, no notifier")
	}
	if !strings.Contains(err.Error(), "session bus") {
		t.Fatalf("err = %v", err)
	}
}

func TestNilNotifier(t *testing.T) {
	var n *Notifier
	if err := n.Notify(context.Background(), "k", "s", "b"); err == nil {
		t.Fatal("a nil notifier cannot notify")
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
}
