# ADR-0003: Use whatsmeow for the WhatsApp protocol, behind a Backend port

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [whatsmeow](https://github.com/tulir/whatsmeow),
  [event pipeline](../architecture/event-pipeline.md),
  [FAQ: account risk](../user/faq.md#is-my-account-at-risk)

## Context

There is no official API that can act as a personal account or a linked
device. The Cloud API and its coexistence mode are business-only, and EU
DMA interoperability is for other messaging *services*, not third-party
clients. The client therefore has to speak the reverse-engineered
multi-device protocol.

whatsmeow:

- is the most widely used Go implementation (mautrix-whatsapp, nchat,
  wacli)
- is MPL-2.0
- is actively maintained, with commits every week
- has no semver releases
- is updated about monthly for protocol changes; stale builds are
  rejected with `ClientOutdated`

## Decision

We will use `go.mau.fi/whatsmeow`, pinned to a pseudo-version, and wrap
it in `internal/wa`, which implements our own `backend.Backend` interface.
whatsmeow types do not cross that boundary.

We enable `SynchronousAck`, `EnableDecryptedEventBuffer` and
success-status handlers, so that a message is persisted before it is
receipted ([event pipeline](../architecture/event-pipeline.md) §1). We
set the device name before pairing. We do not modify whatsmeow's files.
If a patch is ever unavoidable, it stays MPL-2.0 and is sent upstream.

## Alternatives considered

| Option | Why not chosen |
|---|---|
| WhatsApp Cloud API | Cannot be used with a personal account or existing chats and groups |
| Baileys (TS), whatsapp-rust (Rust) | Different language ([ADR-0002](0002-go-as-implementation-language.md)); whatsapp-rust is younger |
| Our own protocol implementation | Enormous effort and risk; it duplicates whatsmeow |

## Consequences

- We inherit whatsmeow's protocol support, and also its churn. A
  scheduled CI job bumps whatsmeow and we release at least monthly
  ([packaging](../architecture/packaging.md) §6).
- Account restriction risk and the ToS conflict are inherent to any
  unofficial client. We disclose them and avoid behaviour that looks
  like abuse ([security](../architecture/security.md) §7).
- The `Backend` port enables a fake backend for tests, benchmarks and a
  `--demo` mode, and keeps a future library switch possible.
- MPL-2.0 is compatible with our GPL-3.0-or-later license
  ([ADR-0007](0007-license-gpl-3.0-or-later.md)).

## Revisit when

- whatsmeow goes more than 3 months without protocol updates while
  `ClientOutdated` errors appear.
- Its license changes.
- An official API that supports personal accounts appears.
