// SPDX-License-Identifier: GPL-3.0-or-later

package fake

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

var (
	_ backend.Linker      = (*Backend)(nil)
	_ backend.Reconnecter = (*Backend)(nil)
)

// Reconnect implements backend.Reconnecter: a simulated network flap ends
// at once.
func (b *Backend) Reconnect() {
	select {
	case b.reconnect <- struct{}{}:
	default:
	}
}

// PairWithPhone implements backend.Linker: the phone "types" the code a
// moment later.
func (b *Backend) PairWithPhone(_ context.Context, phone string) (string, error) {
	if len(phone) < 7 {
		return "", errors.New("the phone number is too short")
	}
	b.linker <- "code"
	return "TARC-1NDE", nil
}

// SubmitPasskey implements backend.Linker.
func (b *Backend) SubmitPasskey(_ context.Context, response string) error {
	if response == "" {
		return errors.New("empty passkey response")
	}
	b.linker <- "passkey"
	return nil
}

// ConfirmLink implements backend.Linker.
func (b *Backend) ConfirmLink(context.Context) error {
	b.linker <- "confirm"
	return nil
}

// link simulates linking a new device. It reports whether the device was
// linked before ctx ended.
func (r *run) link(ctx context.Context) bool {
	cfg := &r.b.cfg
	scan := time.NewTimer(cfg.LinkAfter)
	defer scan.Stop()
	codes := time.NewTicker(cfg.LinkCodeEvery)
	defer codes.Stop()
	n := 0
	showQR := func() {
		n++
		r.deliverEphemeral(ctx, domain.LinkProgress{State: domain.LinkQR, Expires: cfg.LinkCodeEvery,
			QR: fmt.Sprintf("2@fake-link-code-%d,%s,%s", n, r.b.me.LID, r.b.me.PN)})
	}
	wait := func(want string) bool {
		for {
			select {
			case <-ctx.Done():
				return false
			case got := <-r.b.linker:
				if got == want {
					return true
				}
			}
		}
	}
	showQR()
scanned:
	for {
		select {
		case <-ctx.Done():
			return false
		case <-codes.C:
			showQR()
		case <-scan.C:
			break scanned
		case got := <-r.b.linker:
			if got == "code" {
				time.Sleep(cfg.Tick)
				break scanned
			}
		}
	}
	if cfg.LinkPasskey {
		r.deliverEphemeral(ctx, domain.LinkProgress{State: domain.LinkPasskey,
			Challenge: `{"challenge":"ZmFrZQ","rpId":"web.whatsapp.com","timeout":60000}`})
		if !wait("passkey") {
			return false
		}
		r.deliverEphemeral(ctx, domain.LinkProgress{State: domain.LinkConfirm, Code: "481516"})
		if !wait("confirm") {
			return false
		}
	}
	r.deliverDurable(ctx, r.start, []domain.Event{domain.AccountLinked{PN: r.b.me.PN, LID: r.b.me.LID}})
	return ctx.Err() == nil
}
