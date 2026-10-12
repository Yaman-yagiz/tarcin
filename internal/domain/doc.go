// SPDX-License-Identifier: GPL-3.0-or-later

// Package domain defines the core types shared by every layer of tarcin:
// addresses (JIDs), identities, chats, messages and the events that flow
// from the protocol backend into the store and the UI.
//
// The package depends only on the standard library (enforced by
// internal/archtest), so protocol and storage details never leak into it.
// Text fields hold raw, untrusted content; they must be passed through
// internal/sanitize before they are rendered.
package domain
