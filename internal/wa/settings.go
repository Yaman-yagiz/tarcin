// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/Yaman-yagiz/tarcin/internal/backend"
	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

var _ backend.ChatSettings = (*Adapter)(nil)

// MuteChat implements backend.ChatSettings.
func (a *Adapter) MuteChat(ctx context.Context, chat domain.JID, until time.Time) error {
	var end *int64
	switch {
	case until.IsZero():
	case until.Equal(domain.MuteForever):
		end = proto.Int64(-1)
	default:
		end = proto.Int64(until.UnixMilli())
	}
	return a.sendSetting(ctx, chat, func(j types.JID) appstate.PatchInfo { return appstate.BuildMuteAbs(j, !until.IsZero(), end) })
}

// PinChat implements backend.ChatSettings.
func (a *Adapter) PinChat(ctx context.Context, chat domain.JID, pinned bool) error {
	return a.sendSetting(ctx, chat, func(j types.JID) appstate.PatchInfo { return appstate.BuildPin(j, pinned) })
}

// ArchiveChat implements backend.ChatSettings.
func (a *Adapter) ArchiveChat(ctx context.Context, chat domain.JID, archived bool) error {
	return a.sendSetting(ctx, chat, func(j types.JID) appstate.PatchInfo { return appstate.BuildArchive(j, archived, time.Time{}, nil) })
}

func (a *Adapter) sendSetting(ctx context.Context, chat domain.JID, build func(types.JID) appstate.PatchInfo) error {
	j, err := toJID(chat)
	if err != nil {
		return err
	}
	if !a.cli.IsConnected() {
		return backend.ErrNotConnected
	}
	if err := a.cli.SendAppState(ctx, build(j)); err != nil {
		return fmt.Errorf("changing the chat's setting: %w", err)
	}
	return nil
}
