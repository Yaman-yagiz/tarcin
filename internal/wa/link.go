// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// Linking a new device happens inside Run, with the event handler already
// registered: the phone starts the history sync as soon as the link is
// made, and an event dispatched while no handler waits is acknowledged and
// lost (the server deletes history blobs after delivery).

var _ backend.Linker = (*Adapter)(nil)

// forwardLink turns the items of whatsmeow's QR channel into LinkProgress
// events until linking succeeds or stops.
func (a *Adapter) forwardLink(ctx context.Context, sink backend.Sink, items <-chan whatsmeow.QRChannelItem) error {
	failed := func(reason string, err error) error {
		a.deliver(ctx, sink, domain.LinkProgress{State: domain.LinkFailed, Reason: reason})
		if err != nil {
			return fmt.Errorf("%w: %s: %w", backend.ErrLinkFailed, reason, err)
		}
		return fmt.Errorf("%w: %s", backend.ErrLinkFailed, reason)
	}
	var last string
	for item := range items {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			last = item.Code
			a.deliver(ctx, sink, domain.LinkProgress{State: domain.LinkQR, QR: item.Code, Expires: item.Timeout})
		case whatsmeow.QRChannelScannedWithoutMultidevice.Event:
			a.deliver(ctx, sink, domain.LinkProgress{State: domain.LinkQR, QR: last,
				Reason: "The phone needs multi-device enabled; then scan the code again"})
		case whatsmeow.QRChannelEventPasskeyRequest:
			challenge, err := json.Marshal(item.PasskeyRequest.PublicKey)
			if err != nil {
				return failed("the passkey request could not be read", err)
			}
			a.deliver(ctx, sink, domain.LinkProgress{State: domain.LinkPasskey, Challenge: string(challenge)})
		case whatsmeow.QRChannelEventPasskeyResponse:
			a.deliver(ctx, sink, domain.LinkProgress{State: domain.LinkConfirm, Code: item.PasskeyConfirmation.Code})
		case whatsmeow.QRChannelSuccess.Event:
			return nil // AccountLinked comes from the PairSuccess event
		case whatsmeow.QRChannelTimeout.Event:
			return failed("The codes expired before the phone used one", nil)
		case whatsmeow.QRChannelClientOutdated.Event:
			return failed("WhatsApp rejected this version: update tarcin", nil)
		case whatsmeow.QRChannelErrUnexpectedEvent.Event:
			return failed("The connection ended unexpectedly", nil)
		case whatsmeow.QRChannelEventError:
			return failed("The phone could not link this device", item.Error)
		}
	}
	return failed("The connection ended", nil)
}

// PairWithPhone implements backend.Linker. WhatsApp only accepts the
// display name of a common browser here; after linking, the phone shows
// the device name set with SetDeviceName.
func (a *Adapter) PairWithPhone(ctx context.Context, phone string) (string, error) {
	if a.sess.Paired() {
		return "", errors.New("this device is already linked")
	}
	code, err := a.cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	switch {
	case errors.Is(err, whatsmeow.ErrPhoneNumberTooShort):
		return "", errors.New("the phone number is too short")
	case errors.Is(err, whatsmeow.ErrPhoneNumberIsNotInternational):
		return "", errors.New("give the number in international format, with the country code and without a leading 0")
	case err != nil:
		return "", fmt.Errorf("asking for a pairing code: %w", err)
	}
	return code, nil
}

// SubmitPasskey implements backend.Linker. The response is the JSON of a
// WebAuthn assertion, made in the user's browser on web.whatsapp.com:
// no client other than the account owner's authenticator can make it.
func (a *Adapter) SubmitPasskey(ctx context.Context, response string) error {
	var resp types.WebAuthnResponse
	if err := json.NewDecoder(strings.NewReader(response)).Decode(&resp); err != nil {
		return fmt.Errorf("the passkey response is not valid JSON: %w", err)
	}
	if resp.ID == "" || len(resp.Response.Signature) == 0 || len(resp.Response.ClientDataJSON) == 0 {
		return errors.New("the passkey response lacks its ID, signature or client data")
	}
	if err := a.cli.SendPasskeyResponse(ctx, &resp); err != nil {
		return fmt.Errorf("sending the passkey response: %w", err)
	}
	return nil
}

// ConfirmLink implements backend.Linker.
func (a *Adapter) ConfirmLink(ctx context.Context) error {
	if err := a.cli.SendPasskeyConfirmation(ctx); err != nil {
		return fmt.Errorf("confirming the link: %w", err)
	}
	return nil
}
