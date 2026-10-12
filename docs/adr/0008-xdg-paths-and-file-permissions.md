# ADR-0008: Follow the XDG Base Directory spec, with strict permissions

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [XDG Base Directory spec](https://specifications.freedesktop.org/basedir/latest/),
  [data model](../architecture/data-model.md) §1,
  [security](../architecture/security.md) §3

## Context

Arch users expect XDG-compliant applications. The data we store is
sensitive: the device keys alone let an attacker impersonate the device.
whatscli kept its session DB in the *config* directory with default
permissions. That directory is often synced to dotfile repositories.

## Decision

| Kind | Location | Contents |
|---|---|---|
| Config | `$XDG_CONFIG_HOME/tarcin/` | `config.toml` only. **No secrets.** tarcin never rewrites it. |
| Data | `$XDG_DATA_HOME/tarcin/profiles/<name>/` | `session.db`, `tarcin.db`, `lock` |
| State | `$XDG_STATE_HOME/tarcin/` | Logs, compose history, UI state |
| Cache | `$XDG_CACHE_HOME/tarcin/` | Media and thumbnails. Always safe to delete. |
| Runtime | `$XDG_RUNTIME_DIR/tarcin/` | Reserved for a future control socket |

Rules:

- Empty, unset or **relative** values fall back to the spec defaults.
  We implement this ourselves, because Go's `os.User*Dir` returns errors
  for relative paths and has no data or state helpers.
- `umask(0077)` is set at startup. Directories are 0700 and files 0600.
  Modes are verified at startup, and tarcin warns about anything looser.
- A `--profile` flag selects the profile. The MVP has only `default`.
  Profile names are 1–32 lowercase letters, digits, `-` or `_`, so a
  name can never point outside its directory.
- `TARCIN_CONFIG_DIR`, `TARCIN_DATA_DIR`, `TARCIN_STATE_DIR` and
  `TARCIN_CACHE_DIR` replace a whole tarcin directory, for tests and
  portable setups. They must be absolute.
- The single-instance lock is an `flock` on `profiles/<name>/lock` in the
  data directory, next to the databases it protects. The kernel releases
  it when the process dies, so a crash never leaves a stale lock.

Implemented in `internal/paths`.

## Alternatives considered

| Option | Why not chosen |
|---|---|
| Everything in `~/.config/tarcin` (the whatscli approach) | Mixes secrets into a directory that users sync and back up; violates XDG |
| `~/.tarcin` | Not XDG; clutters `$HOME` |

## Consequences

- Backups of `~/.local/share/tarcin` contain device keys. The docs must
  say so, and `tarcin backup` excludes the session DB by default.
- Users can wipe the cache without losing anything.

## Revisit when

- We add a daemon mode. Then define the runtime socket permissions in a
  new ADR.
