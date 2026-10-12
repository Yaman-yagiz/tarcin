# Security and privacy

This document is tarcin's threat model and the controls that follow from
it. How to report vulnerabilities is described in
[SECURITY.md](../../SECURITY.md).

## 1. Assets

| Asset | Where | Impact if leaked |
|---|---|---|
| Device identity and Signal state (identity key, noise key, pre-keys, sessions, sender keys, app-state keys) | `session.db` | **Critical.** The attacker can impersonate the linked device and read new messages. |
| Message history, media, contacts, group membership | `tarcin.db`, media cache | High: private communication |
| Phone numbers and LIDs of contacts | Both DBs | High: personal data of third parties |
| Logs and crash output | State dir, stderr | Leaks content if logging is careless |
| Build and release chain | GitHub, AUR | A compromise affects every user |

## 2. Adversaries and scope

| Adversary | In scope? | Notes |
|---|---|---|
| Malicious or malformed **message content** from other WhatsApp users | **Yes** | Terminal escape injection, spoofing with bidi or zero-width characters, oversized payloads, hostile file names, malformed media |
| **Other local users** on the same machine | **Yes** | Covered by file modes and umask |
| **Theft of a powered-off device** or a disk image | Yes, via the OS | We rely on full-disk encryption ([ADR-0010](../adr/0010-encryption-at-rest-deferred.md)). App-level encryption may be added later. |
| **Copied backups** of the home directory | Partly | We document that `session.db` must never be backed up to untrusted places. `tarcin backup` excludes it by default. |
| **Malware running as the same user** | **No** | It can read process memory, the keyring and the files. No user-space client can defend against it. |
| **Network attackers** | Delegated | Transport security is the Noise handshake and Signal protocol, implemented by whatsmeow. We do not alter cryptographic code. |
| **Supply-chain attackers** (dependencies, CI, AUR) | **Yes** | See §6 |
| **WhatsApp/Meta** (account restriction) | Risk, not a threat | See §7 |

## 3. Controls: local data

1. **umask 0077** is set at process start. Directories are created 0700
   and files 0600, by their own modes too: a test runs tarcin under a
   0022 umask and checks everything it wrote. Modes are verified at
   startup, and tarcin warns about anything looser.
2. **No secrets in the config file.** It is user-editable and is often
   committed to dotfile repositories.
3. **Single-instance lock** per profile. Two processes on one session
   corrupt Signal state and look suspicious to the server.
4. **SQLite `secure_delete=ON`** on the app DB, so deleted rows are
   overwritten. The WAL still holds them, so after a chat is deleted or
   cleared, messages expire or a message is deleted for everyone, the
   writer checkpoints the WAL and truncates it once it is idle. A test
   checks that a deleted chat's text and its full-text index words are
   then in neither file.
5. **Temporary files** are created in private 0700 directories and
   deleted afterwards: `$EDITOR` drafts in the profile's state directory
   (`profiles/<name>/drafts`), partial downloads and uploads in its media
   cache. A crash leaves them behind, so the next start removes them,
   under the profile's lock. Tests check that a failed download, a sent
   attachment and an edited draft leave none.
6. **Logout** (`tarcin logout`) unlinks the device on the server, then
   deletes `session.db` and its WAL/SHM files, then offers to delete
   messages, their pre-migration snapshots and media (default: keep).
   If the server cannot be reached, it asks before deleting the keys,
   because the phone would go on listing the device until it is removed
   there. It takes the profile lock, so it never runs beside tarcin.

## 4. Controls: untrusted input

1. **Terminal-safe rendering.** Every network-originated string passes
   through `internal/sanitize` before it reaches the screen (see
   [UI](ui.md) §7). The sanitiser is fuzzed, and so is what it guards:
   the message list is drawn from fuzzed text, names and widths
   (`FuzzRender`), and fuzzed protocol messages and history syncs go
   through normalisation (`FuzzNormalizeMessage`, `FuzzHistory`).
2. **Size limits.**
   - Message bodies are rendered up to a limit and cut off beyond it.
   - Media downloads respect a configurable maximum size (default
     100 MiB), checked against the size declared in the message *and*
     enforced while streaming.
   - History-sync payload handling is bounded, as described in
     [event pipeline](event-pipeline.md) §3.1.
3. **File names are never used as paths.**
   - Cached media is stored under a content-addressed name
     (`sha256.ext`), where the extension comes from an allow-list keyed
     by MIME type.
   - When the user saves a file, the suggested name is passed through
     `filepath.Base`, stripped of control and bidi characters and
     leading dots, and saved with `O_EXCL` into the chosen directory.
     tarcin never overwrites silently.
4. **MIME types.** The declared MIME type is untrusted. The opener is
   chosen by the user's configuration, and files are never executed or
   marked executable: cache files and saved copies are 0600.
   Implemented in `internal/app/media.go`: the streaming limit wraps the
   file whatsmeow writes, so no write, seek or truncate grows it past the
   limit; the cache keeps to `media.cache_mib`, dropping the least
   recently opened files first.
5. **Atomic writes.** Data goes to a temp file in the same directory, is
   `fsync`ed, and is then renamed into place.
6. **No shell.** External programs (`$EDITOR`, `$PAGER`, `xdg-open`,
   openers) are run with `exec.Command(name, args...)`. Message content
   is never interpolated into a shell string.
7. **SQL is always parameterised.** DSNs are built with `net/url`.
8. **Who may change a message is checked by the client.** The protocol
   is end-to-end encrypted, so the server cannot check it, and a member
   could otherwise rewrite or hide other people's messages. An edit is
   accepted only from the message's sender; a revoke from its sender, or
   from a known admin of the group when it is marked as an admin's. The
   check also holds for an edit or revoke that waited for its message.
   Refused changes are counted, never applied.
9. **History arrives only from this account's phone.** A history-sync
   notification from anyone else is ignored: its inline payload could
   otherwise inject messages into any chat.

## 5. Controls: logging and diagnostics

1. Structured logging (`log/slog`, text lines) goes to
   `$XDG_STATE_HOME/tarcin/tarcin.log` with mode 0600 (`internal/logging`).
   At 5 MiB it is renamed to `tarcin.log.1`, replacing the previous one,
   so at most 10 MiB is kept. The demo logs nothing.
2. **Never logged:** message bodies, captions, media keys, key material,
   QR or pairing codes, names, or full phone numbers. Code passes none of
   them to a logger; the handler is the second line of defense:
   - Every message and attribute is redacted. WhatsApp addresses become
     `jid#a1b2c3` and runs of eight or more digits `num#a1b2c3`: an
     HMAC-SHA-256 under a random key that lives only in memory, so lines
     of one run can be followed, but a hash cannot be reversed by trying
     every phone number. All devices of a person hash the same.
   - Attributes such as `body`, `text`, `qr`, `code`, `phone` and
     `push_name` are written as `[redacted]`, whatever they hold.
   - Values of other types are written as redacted text, so no type's
     formatting bypasses the filter; numbers, times and durations pass.
   - Protocol node dumps keep their tag names only, and every line is
     cut at 2 KiB.

   There is no unredacted mode.
3. whatsmeow's logger is wrapped with the same redaction filter. Its
   debug lines are always dropped: they dump protocol nodes, with names
   and message metadata. Tests inject canary text, names and addresses
   through every logging path and fail if any reaches the log.
4. **Panics.** A panic in the UI, in a command it runs or in one of
   tarcin's goroutines stops the program the normal way, which restores
   the terminal, and tarcin prints where the log is. The log gets where
   it happened, the panic's type (or a runtime error's text) and the
   stack as functions and `file:line`, never the panic's value, which
   could hold message content. A panic while applying events fails only
   that submission, and is logged the same way (`internal/app/crash.go`).
5. pprof is opt-in (`--pprof`) and bound to a loopback address only (see
   [performance](performance.md) §5). Other local users can still reach
   a loopback port, so it is for development; profiles hold code
   locations and numbers, not message content. tarcin opens **no other
   listening sockets**.
6. **No telemetry.** Adding any would require an ADR, an explicit opt-in
   and a privacy notice.

## 6. Controls: supply chain and releases

1. `go.sum` is committed. Dependencies are kept to what is needed, and
   every new dependency is justified in its PR. New dependency versions
   are at least two weeks old, so that a compromised release has time
   to be noticed. The exception is a security fix that `govulncheck`
   reports, from the Go project's own modules (`golang.org/x/...`): it
   is taken at once. (`golang.org/x/net` v0.60.0 was taken three days
   after its release for five HTTP/2 advisories.)
2. CI runs:
   - `go vet`
   - `staticcheck`
   - `govulncheck ./...`
   - `go test -race`
   - every fuzz target for a short duration (`make fuzz-short`, 5 s
     each, part of `make ci`), including the config parser (`FuzzParse`)
   - a license check of all dependencies (GPL-3.0-compatible only)
3. CI actions are pinned by commit SHA, and the token permissions are
   read-only by default.
4. **Releases** are built from signed tags with `-trimpath`, with no
   embedded timestamps (`SOURCE_DATE_EPOCH`). Reproducibility is checked
   by building twice and comparing. A `SHA256SUMS` file and signatures
   are published.
5. The AUR package verifies the signatures (`validpgpkeys`) and builds
   from source with the Arch Go flags (see [packaging](packaging.md)).
6. whatsmeow is bumped regularly, and a security
   release follows any CVE in a Go or dependency release.

## 7. Account safety (user-facing)

tarcin is an unofficial client, and WhatsApp's
[Terms of Service](https://www.whatsapp.com/legal/terms-of-service) do
not allow unofficial clients. The README and the first-run screen say
so. The product rules are:

- No bulk sending, broadcast tools, scraping, auto-replies or scheduled
  messages.
- No background enumeration of groups, contacts or presence beyond what
  the official clients do.
- Protocol requests from background jobs are rate-limited.
- A phone number is looked up on the server only when the user types it
  to start a chat (`:new +NUMBER`), one number per command, as the
  official clients do; numbers are never looked up in bulk.
- Privacy toggles exist for read receipts, typing indicators and
  presence: `privacy.read_receipts`, `privacy.typing` and
  `privacy.presence` in `config.toml`, all on by default as in WhatsApp.
  Even on, tarcin is online only while the user is active: from a key
  press until two minutes without one or until the terminal loses focus,
  and it goes offline when it quits. A terminal left open does not show
  the account online all day. With presence off, tarcin never follows
  anyone's online status either.

## 8. Open questions

- App-level encryption at rest: ncruces adiantum versus SQLCipher,
  and how to manage the key (Secret Service, or a passphrase with
  Argon2id).
- Whether to verify a reproducible build independently in CI from a
  second builder.
