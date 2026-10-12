# ADR-0010: Rely on full-disk encryption in v1; defer app-level encryption at rest

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [security](../architecture/security.md) §2,
  [data and privacy](../user/data.md)

## Context

Both databases are sensitive. Anyone who copies `session.db` can act as
the linked device. The options for encrypting them inside the application
have real costs:

- **SQLCipher.** The Go bindings are stale (from 2020), and mattn has no
  hook for it, so we would have to maintain a cgo fork.
- **ncruces adiantum/xts VFS.** Pure Go, but the encryption is
  deterministic and gives no tamper protection. Both databases would have
  to move to ncruces, and the overhead has not been measured.
- **Key management.** A Secret Service keyring is readable by any process
  running as the same user while the session is unlocked. A
  passphrase-derived key adds friction at every start. A key file next to
  the DB, or one derived from `/etc/machine-id`, is obfuscation, not
  security.

Without key management that protects against the realistic adversaries,
app-level encryption mainly protects copies and backups. Full-disk
encryption already covers the powered-off-device case.

## Decision

For v1:

- tarcin relies on **full-disk encryption** (LUKS) plus strict file
  permissions ([ADR-0008](0008-xdg-paths-and-file-permissions.md)).
- `secure_delete` is on, and `tarcin backup` excludes the session DB by
  default.
- The README and the first-run screen state plainly that local data is
  **not** encrypted by tarcin and recommend full-disk encryption.
- tarcin **never ships fake encryption.**

Optional app-level encryption will be evaluated after v1.

## Alternatives considered

The alternatives are listed in the context section above.

## Consequences

- The v1 implementation stays simple, and the security claims we make
  are honest.
- Users without full-disk encryption are exposed if their disk is stolen.
  The documentation warns them.

## Revisit when

- Users or packagers request it with concrete threat scenarios.
- ncruces reaches v1, or a maintained SQLCipher binding for
  `database/sql` appears.
- An evaluation finds acceptable overhead and a key-management design.
