// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/ui/linkscreen"
)

// linkTimeout bounds one request of the link screen to the backend.
const linkTimeout = 30 * time.Second

// linkOptions returns the link screen's actions for a backend that can
// link a device, or nil. Retry asks runBackend for a new attempt.
func linkOptions(b backend.Backend, retry chan<- struct{}) *linkscreen.Options {
	l, ok := b.(backend.Linker)
	if !ok {
		return nil
	}
	return &linkscreen.Options{
		PairWithPhone: func(phone string) tea.Cmd {
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), linkTimeout)
				defer cancel()
				code, err := l.PairWithPhone(ctx, phone)
				return linkscreen.CodeMsg{Code: code, Err: err}
			}
		},
		SubmitPasskey: func(response string) tea.Cmd {
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), linkTimeout)
				defer cancel()
				return linkscreen.ResultMsg{Err: l.SubmitPasskey(ctx, response)}
			}
		},
		ConfirmLink: func() tea.Cmd {
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), linkTimeout)
				defer cancel()
				return linkscreen.ResultMsg{Err: l.ConfirmLink(ctx)}
			}
		},
		Retry: func() tea.Cmd {
			select {
			case retry <- struct{}{}:
			default:
			}
			return nil
		},
	}
}

// runBackend runs b until ctx ends. When linking a new device fails, it
// waits for the user to ask for another attempt instead of showing new
// codes to someone who may have walked away.
func runBackend(ctx context.Context, b backend.Backend, sink backend.Sink, retry <-chan struct{}) error {
	for {
		err := b.Run(ctx, sink)
		if !errors.Is(err, backend.ErrLinkFailed) {
			return err
		}
		select {
		case <-retry:
		case <-ctx.Done():
			return nil
		}
	}
}

// reconnectOption returns ui.Options.Reconnect for a backend that can
// connect again at once, or nil.
func reconnectOption(b backend.Backend) func() tea.Cmd {
	r, ok := b.(backend.Reconnecter)
	if !ok {
		return nil
	}
	return func() tea.Cmd {
		r.Reconnect()
		return nil
	}
}
