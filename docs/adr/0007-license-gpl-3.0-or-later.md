# ADR-0007: License tarcin under GPL-3.0-or-later

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [LICENSE](../../LICENSE), [CONTRIBUTING](../../CONTRIBUTING.md)

## Context

tarcin handles private messages and device keys. Users benefit from being
able to audit every build they run, including forks. The main
dependencies have these licenses:

| Dependency | License | Compatible with GPL-3.0-or-later? |
|---|---|---|
| whatsmeow | MPL-2.0 | Yes. MPL-2.0 §3.3 allows combining it with GPL code; whatsmeow's own files stay MPL |
| Bubble Tea, Bubbles, Lip Gloss | MIT | Yes |
| mattn/go-sqlite3 | MIT | Yes |
| SQLite | Public domain | Yes |
| godbus/dbus | BSD-2-Clause | Yes |
| golang.org/x/* | BSD-3-Clause | Yes |

Note that whatscli has no LICENSE file, so its code must not be copied.

## Decision

tarcin is licensed **GPL-3.0-or-later**:

- The `LICENSE` file contains the canonical GPL-3.0 text.
- Every Go source file starts with an SPDX header:
  `// SPDX-License-Identifier: GPL-3.0-or-later`.
- The PKGBUILD uses `license=('GPL-3.0-or-later')`.
- Packaging files under `packaging/aur/` are 0BSD, as Arch RFC 40/52
  expect.

Contributions are accepted under the same license, with a Developer
Certificate of Origin sign-off (`git commit -s`). No CLA is required.

## Alternatives considered

| Option | Why not chosen |
|---|---|
| MIT or Apache-2.0 | Permissive. They allow closed-source forks of a client that handles private data. |
| MPL-2.0 | File-level copyleft only. New files in a fork could be closed. |
| AGPL-3.0 | Its network clause is irrelevant for a local client and only adds friction. |

## Consequences

- Each new dependency must have a GPL-3.0-compatible license. CI checks
  this (`go-licenses` or an equivalent).
- Redistributors of binaries must provide the source, including
  whatsmeow's MPL source, which a `go mod` based build provides
  naturally.

## Revisit when

- A required dependency is only available under an incompatible license.
