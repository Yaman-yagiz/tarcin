// SPDX-License-Identifier: GPL-3.0-or-later

// Package wa is the WhatsApp adapter: it implements backend.Backend on
// whatsmeow (docs/adr/0003-whatsmeow-protocol-library.md). whatsmeow is
// imported only here, and its types are converted to domain types at the
// boundary (both enforced by internal/archtest), so that the protocol
// library can be replaced.
//
// The device store lives in session.db, opened by the caller with the
// store's SQLite driver and handed over as a *sql.DB: the adapter never
// imports the driver.
package wa
