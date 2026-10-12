// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

func testAdapter(t testing.TB) *Adapter {
	t.Helper()
	s := openTestSession(t, filepath.Join(t.TempDir(), "session.db"))
	t.Cleanup(func() { _ = s.Close() })
	return New(s, Options{DeliverTimeout: time.Second})
}

func feed(items ...whatsmeow.QRChannelItem) <-chan whatsmeow.QRChannelItem {
	ch := make(chan whatsmeow.QRChannelItem, len(items))
	for _, it := range items {
		ch <- it
	}
	close(ch)
	return ch
}

func TestForwardLinkSuccess(t *testing.T) {
	a, sink := testAdapter(t), &recordingSink{}
	err := a.forwardLink(t.Context(), sink, feed(
		whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "2@first", Timeout: time.Minute},
		whatsmeow.QRChannelScannedWithoutMultidevice,
		whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "2@second", Timeout: 20 * time.Second},
		whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventPasskeyRequest, PasskeyRequest: &events.PairPasskeyRequest{
			PublicKey: &types.WebAuthnPublicKey{Challenge: []byte{1, 2, 3}, RelyingPartID: "web.whatsapp.com", Timeout: 60000},
		}},
		whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventPasskeyResponse, PasskeyConfirmation: &events.PairPasskeyConfirmation{Code: "123456"}},
		whatsmeow.QRChannelSuccess,
	))
	if err != nil {
		t.Fatal(err)
	}
	var got []domain.LinkProgress
	for _, ev := range sink.events {
		got = append(got, ev.(domain.LinkProgress))
	}
	if len(got) != 5 {
		t.Fatalf("progress = %+v", got)
	}
	if got[0].State != domain.LinkQR || got[0].QR != "2@first" || got[0].Expires != time.Minute {
		t.Errorf("first QR = %+v", got[0])
	}
	if got[1].QR != "2@first" || !strings.Contains(got[1].Reason, "multi-device") {
		t.Errorf("a scan without multi-device keeps the code with a hint: %+v", got[1])
	}
	if got[3].State != domain.LinkPasskey || !strings.Contains(got[3].Challenge, `"rpId":"web.whatsapp.com"`) {
		t.Errorf("passkey request = %+v", got[3])
	}
	if got[4].State != domain.LinkConfirm || got[4].Code != "123456" {
		t.Errorf("confirmation = %+v", got[4])
	}
}

func TestForwardLinkFailures(t *testing.T) {
	for name, items := range map[string][]whatsmeow.QRChannelItem{
		"timeout":  {{Event: whatsmeow.QRChannelEventCode, Code: "2@x"}, whatsmeow.QRChannelTimeout},
		"outdated": {whatsmeow.QRChannelClientOutdated},
		"refused":  {{Event: whatsmeow.QRChannelEventError, Error: errors.New("pair error")}},
		"closed":   {},
	} {
		a, sink := testAdapter(t), &recordingSink{}
		err := a.forwardLink(t.Context(), sink, feed(items...))
		if !errors.Is(err, backend.ErrLinkFailed) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		last := sink.events[len(sink.events)-1].(domain.LinkProgress)
		if last.State != domain.LinkFailed || last.Reason == "" {
			t.Errorf("%s: the UI must learn why: %+v", name, last)
		}
	}
}

func TestPairSuccessIsDurable(t *testing.T) {
	a, sink := testAdapter(t), &recordingSink{}
	ok := a.handle(t.Context(), sink, &events.PairSuccess{
		ID:  types.JID{User: "905551112233", Device: 7, Server: types.DefaultUserServer},
		LID: types.JID{User: "123", Device: 7, Server: types.HiddenUserServer}, BusinessName: "Shop",
	})
	if !ok || len(sink.events) != 1 {
		t.Fatalf("handled %v, events %+v", ok, sink.events)
	}
	want := domain.AccountLinked{PN: "905551112233@s.whatsapp.net", LID: "123@lid", BusinessName: "Shop"}
	if sink.events[0] != want {
		t.Fatalf("event = %+v, want %+v", sink.events[0], want)
	}
}

func TestLinkerInputs(t *testing.T) {
	a := testAdapter(t)
	for _, resp := range []string{"", "not json", `{"id":""}`, `{"id":"x","response":{"clientDataJSON":"AQ"}}`} {
		if err := a.SubmitPasskey(t.Context(), resp); err == nil {
			t.Errorf("SubmitPasskey(%q) must fail before sending", resp)
		}
	}
	if _, err := a.PairWithPhone(t.Context(), "123"); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Errorf("short number: %v", err)
	}
	if _, err := a.PairWithPhone(t.Context(), "05551112233"); err == nil || !strings.Contains(err.Error(), "international") {
		t.Errorf("national number: %v", err)
	}
	a.sess.device.ID = &types.JID{User: "905551112233", Server: types.DefaultUserServer}
	if _, err := a.PairWithPhone(t.Context(), "905551112233"); err == nil || !strings.Contains(err.Error(), "already linked") {
		t.Errorf("linked device: %v", err)
	}
}

func TestNumberResult(t *testing.T) {
	lid, pn := types.NewJID("77", types.HiddenUserServer), types.NewJID("905551112233", types.DefaultUserServer)
	jid, evs, err := numberResult([]types.IsOnWhatsAppResponse{{JID: lid, PhoneNumber: pn, IsIn: true}})
	if err != nil || jid != "77@lid" || len(evs) != 1 || evs[0] != (domain.IdentityLinked{PN: "905551112233@s.whatsapp.net", LID: "77@lid"}) {
		t.Fatalf("with a LID: %q, %v, %v", jid, evs, err)
	}
	// Without a LID the phone-number JID addresses the chat.
	jid, evs, err = numberResult([]types.IsOnWhatsAppResponse{{JID: pn, IsIn: true}})
	if err != nil || jid != "905551112233@s.whatsapp.net" || len(evs) != 0 {
		t.Fatalf("without a LID: %q, %v, %v", jid, evs, err)
	}
	for _, res := range [][]types.IsOnWhatsAppResponse{nil, {{JID: lid, PhoneNumber: pn, IsIn: false}}, {{IsIn: true}}} {
		if _, _, err := numberResult(res); !errors.Is(err, backend.ErrNotOnWhatsApp) {
			t.Fatalf("%+v: err = %v", res, err)
		}
	}
}
