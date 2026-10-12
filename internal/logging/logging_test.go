// SPDX-License-Identifier: GPL-3.0-or-later

package logging

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type jid string // like domain.JID: a named string type

func TestRedactorString(t *testing.T) {
	r := NewRedactor()
	for in, gone := range map[string][]string{
		"Error decrypting message 3EB0A1 from 905551234567:12@s.whatsapp.net in 120363012345678901@g.us":                                {"905551234567", "120363012345678901"},
		"sender 123456789012345@lid, phone +905551234567":                                                                               {"123456789012345", "905551234567"},
		`Node handling took 2s for <message from="905551234567@s.whatsapp.net" notify="Ayşe Yılmaz"><body>secret text</body></message>`: {"Ayşe", "secret text", "905551234567"},
	} {
		out := r.String(in)
		for _, g := range gone {
			if strings.Contains(out, g) {
				t.Errorf("%q leaks %q: %q", in, g, out)
			}
		}
	}
	out := r.String("from 905551234567:12@s.whatsapp.net and 905551234567@s.whatsapp.net, message 3EB0C1D2E3F4")
	hashes := regexp.MustCompile(`jid#[0-9a-f]{6}`).FindAllString(out, -1)
	if len(hashes) != 2 || hashes[0] != hashes[1] {
		t.Fatalf("every device of a person hashes the same: %q", out)
	}
	if !strings.Contains(out, "3EB0C1D2E3F4") {
		t.Fatalf("message IDs are kept: %q", out)
	}
	if other := NewRedactor().String("905551234567@s.whatsapp.net"); strings.Contains(out, other) {
		t.Fatal("a new key must give new hashes")
	}
	if out := r.String("took 1.5s, 20 messages, at 2026-10-11 12:00"); out != "took 1.5s, 20 messages, at 2026-10-11 12:00" {
		t.Fatalf("ordinary text must stay: %q", out)
	}
	if out := r.String(strings.Repeat("ş", 3000)); len(out) > maxText+len("…") || !strings.HasSuffix(out, "…") {
		t.Fatalf("long text must be cut: %d bytes", len(out))
	}
}

// TestHandlerRedacts injects canary content through every route a value
// can take into a record (guarantee for docs/architecture/security.md §5).
func TestHandlerRedacts(t *testing.T) {
	const (
		body = "CANARY-BODY-7f3a"
		pn   = "905559876543"
		lid  = "987654321098765"
	)
	var buf bytes.Buffer
	l := slog.New(NewHandler(&buf, slog.LevelDebug))
	l = l.With("chat", jid(pn+"@s.whatsapp.net"))
	l.Info("received "+lid+"@lid",
		"body", body, "TEXT", body, "qr", "2@"+body, "code", "ABCD-"+body,
		"err", fmt.Errorf("sending to %s@s.whatsapp.net: %w", pn, errors.New("timeout")),
		"who", jid(lid+"@lid"), "list", []string{pn + "@s.whatsapp.net"},
		slog.Group("peer", "phone", pn, "id", lid+"@lid"),
		"n", 42, "took", 1500*time.Millisecond, "ok", true)
	l.WithGroup("history").Warn("chunk failed", "from", pn+"@s.whatsapp.net")
	out := buf.String()
	for _, canary := range []string{body, pn, lid} {
		if strings.Contains(out, canary) {
			t.Fatalf("the log leaks %q:\n%s", canary, out)
		}
	}
	for _, want := range []string{"body=[redacted]", "err=\"sending to jid#", "n=42", "took=1.5s", "ok=true", "history.from=jid#"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the log lacks %q:\n%s", want, out)
		}
	}
	buf.Reset()
	slog.New(NewHandler(&buf, nil)).Debug("noise")
	if buf.Len() != 0 {
		t.Fatal("the default level is info")
	}
}

func TestOpenRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tarcin.log")
	l, err := Open(path, Options{MaxSize: 300})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		l.Info("line", "i", i)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l.Info("after close") // dropped, no panic
	for _, f := range []string{path, path + ".1"} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 || info.Size() > 300 {
			t.Fatalf("%s: mode %v, size %d", filepath.Base(f), info.Mode(), info.Size())
		}
	}
	data, _ := os.ReadFile(path) //nolint:gosec // the test's own file
	if !strings.Contains(string(data), "i=9") || strings.Contains(string(data), "after close") {
		t.Fatalf("the newest lines go to the current file:\n%s", data)
	}
	// Reopening appends.
	l, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	l.Info("again")
	_ = l.Close()
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "i=9") || !strings.Contains(string(data), "again") { //nolint:gosec // the test's own file
		t.Fatalf("reopening must append:\n%s", data)
	}
	Discard().Error("nothing")
}
