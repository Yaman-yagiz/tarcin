// SPDX-License-Identifier: GPL-3.0-or-later

// Package logging sets up tarcin's log (docs/architecture/security.md §5):
// log/slog text lines in a private file that rotates by size, with every
// message and attribute redacted on the way. Message content, keys and
// codes must never be passed to a logger; the redaction is the second
// line of defense, for addresses and phone numbers inside error texts
// and library messages.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
)

// DefaultMaxSize is the size at which the log is rotated.
const DefaultMaxSize = 5 << 20

// Options configure a log.
type Options struct {
	Level   slog.Leveler // default slog.LevelInfo
	MaxSize int64        // default DefaultMaxSize
}

// Log is an open log file.
type Log struct {
	*slog.Logger
	file *rotator
}

// Open opens the log at path for appending, creating it with mode 0600.
// When it would grow past MaxSize, it is renamed to path.1 and a new one
// is started, so at most twice MaxSize is kept.
func Open(path string, o Options) (*Log, error) {
	if o.MaxSize <= 0 {
		o.MaxSize = DefaultMaxSize
	}
	f, err := openRotator(path, o.MaxSize)
	if err != nil {
		return nil, err
	}
	return &Log{Logger: slog.New(NewHandler(f, o.Level)), file: f}, nil
}

// Close closes the log file. Lines logged afterwards are dropped.
func (l *Log) Close() error { return l.file.Close() }

// Discard returns a logger that drops everything.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// secretKeys are attribute keys whose values are never written, whatever
// they hold.
var secretKeys = []string{
	"body", "text", "caption", "content", "qr", "code", "pairing_code", "passkey", "key", "secret",
	"phone", "push_name", "contact_name", "subject", "description",
}

// NewHandler returns a handler that redacts every record and writes it
// to w as a slog text line.
func NewHandler(w io.Writer, level slog.Leveler) slog.Handler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &handler{inner: slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}), r: NewRedactor()}
}

type handler struct {
	inner slog.Handler
	r     *Redactor
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, h.r.String(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.attr(a)
	}
	return &handler{inner: h.inner.WithAttrs(clean), r: h.r}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{inner: h.inner.WithGroup(name), r: h.r}
}

// attr redacts one attribute: secret keys lose their value, text is
// redacted, and values of other types are written as redacted text, so
// that no type's formatting can bypass the filter. Numbers, times,
// durations and booleans pass as they are.
func (h *handler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	if slices.Contains(secretKeys, strings.ToLower(a.Key)) {
		return slog.String(a.Key, "[redacted]")
	}
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.r.String(v.String()))
	case slog.KindGroup:
		group := v.Group()
		clean := make([]any, len(group))
		for i, g := range group {
			clean[i] = h.attr(g)
		}
		return slog.Group(a.Key, clean...)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, h.r.String(err.Error()))
		}
		return slog.String(a.Key, h.r.String(fmt.Sprint(v.Any())))
	}
	return slog.Attr{Key: a.Key, Value: v}
}
