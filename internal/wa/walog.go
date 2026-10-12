// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"fmt"
	"log/slog"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// waLogger passes whatsmeow's log lines to a slog logger, whose handler
// redacts them (internal/logging). Debug lines are dropped whatever the
// level: they dump protocol nodes, with names and message metadata, at a
// rate no log should keep.
type waLogger struct {
	l      *slog.Logger
	module string // whatsmeow's module path, such as "whatsmeow/Client"
}

// newWALogger returns a whatsmeow logger writing to l, or whatsmeow's
// no-op logger for nil.
func newWALogger(l *slog.Logger, module string) waLog.Logger {
	if l == nil {
		return waLog.Noop
	}
	return &waLogger{l: l, module: module}
}

func (w *waLogger) log(level slog.Level, msg string, args []any) {
	if !w.l.Enabled(context.Background(), level) {
		return // nothing is formatted for a line that is not written
	}
	w.l.Log(context.Background(), level, fmt.Sprintf(msg, args...), "module", w.module)
}

func (w *waLogger) Errorf(msg string, args ...any) { w.log(slog.LevelError, msg, args) }
func (w *waLogger) Warnf(msg string, args ...any)  { w.log(slog.LevelWarn, msg, args) }
func (w *waLogger) Infof(msg string, args ...any)  { w.log(slog.LevelInfo, msg, args) }
func (w *waLogger) Debugf(string, ...any)          {}

func (w *waLogger) Sub(module string) waLog.Logger {
	return &waLogger{l: w.l, module: w.module + "/" + module}
}
