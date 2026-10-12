# ADR-0009: Use the LID as the canonical user identity, with PN aliases

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [data model](../architecture/data-model.md) §2

## Context

WhatsApp is migrating from phone-number JIDs (PN, `…@s.whatsapp.net`) to
LIDs (`…@lid`). Since 2026-07, whatsmeow sends all DMs to the LID.
Messages, history sync and group participant lists can refer to the same
person by either form, sometimes with the alternative form attached
(`SenderAlt`, `phoneNumberToLidMappings`). Clients that used the raw JID
as their key ended up with duplicate, split chats (nchat #401/#402, wacli
#348/#352/#444). Phone numbers can also be missing or redacted.

## Decision

- Each person is exactly one row in `identities`. The **LID is canonical**
  once it is known. The PN is an optional attribute.
- Every JID we see maps to an identity through `jid_aliases`.
- DM chats are keyed by the peer's canonical identity.
- Resolution uses only local data: our alias table, then whatsmeow's
  `Store.LIDs`. It never makes a network call on the ingest path.
- When a mapping is learned, the writer **merges** the two identities, and
  their DM chats, atomically and idempotently.
- Display names are resolved at render time.

## Alternatives considered

| Option | Why not chosen |
|---|---|
| Key on the raw JID string | Splits chats; the problem documented in other clients |
| Key on the phone number | PNs can be missing or redacted; WhatsApp is moving away from them |
| Always look up the LID over the network before storing | Puts network calls on the ingest path (the whatscli F5 failure mode) |

## Consequences

- The merge logic is the most delicate part of the store. It needs
  property tests that apply any order of PN, LID and mapping events and
  check for one chat with no duplicate messages.
- The UI must cope with an identity's display name and phone number
  appearing later.

## Revisit when

- WhatsApp completes the migration and PN JIDs disappear from the
  protocol. The alias table then becomes legacy.
