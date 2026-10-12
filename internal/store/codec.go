// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"database/sql"
	"encoding/json"
	"math"
	"time"

	"github.com/Yaman-yagiz/tarcin/internal/domain"
)

// Conversions between domain values and their columns
// (docs/architecture/data-model.md §3). Times are Unix milliseconds,
// where 0 or NULL means "none".

func fromMs(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func fromNullMs(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return fromMs(n.Int64)
}

// mutedForever is the muted_until value of a chat muted without an end.
const mutedForever = -1

func mutedFromSQL(n sql.NullInt64) time.Time {
	if n.Valid && n.Int64 == mutedForever {
		return domain.MuteForever
	}
	return fromNullMs(n)
}

var chatKinds = map[string]domain.ChatKind{
	"dm":         domain.ChatDirect,
	"group":      domain.ChatGroup,
	"broadcast":  domain.ChatBroadcast,
	"newsletter": domain.ChatNewsletter,
}

// messageKinds maps the kind column, which holds MessageKind.String(),
// back to the kind.
var messageKinds = func() map[string]domain.MessageKind {
	m := map[string]domain.MessageKind{}
	for k := domain.KindText; k <= domain.KindUnsupported; k++ {
		m[k.String()] = k
	}
	return m
}()

func messageKindFromSQL(s string) domain.MessageKind {
	if k, ok := messageKinds[s]; ok {
		return k
	}
	return domain.KindUnsupported // written by a newer tarcin
}

// u32 and u8 convert integer columns, mapping out-of-range values (a
// damaged or foreign database) to zero instead of wrapping around.
func u32(v int64) uint32 {
	if v < 0 || v > math.MaxUint32 {
		return 0
	}
	return uint32(v)
}

func u8(v int64) uint8 {
	if v < 0 || v > math.MaxUint8 {
		return 0
	}
	return uint8(v)
}

// idList encodes IDs as a JSON array for json_each, which lets one
// statement take any number of IDs without building SQL.
func idList[T ~int64](ids []T) string {
	b, _ := json.Marshal(ids) // integers always marshal
	return string(b)
}
