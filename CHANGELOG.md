# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- WhatsApp through whatsmeow:
  - linking by QR code, pairing code or passkey hand-off, with a
    first-run notice about the account risk and unencrypted data
  - text, captions, quotes and placeholders for every other kind of
    message; edits, deletions and reactions are stored
  - history sync that is stored before it is acknowledged and resumes
    after a restart
  - contact and push names, pins, mutes and archives from app state
  - sending text in order per chat, with retry of failed messages
  - delivery and read ticks, and read receipts behind a setting
  - the connection state in the status line, quick reconnects when the
    network returns, and `:reconnect`
- The terminal UI:
  - a virtualized chat list and message view
  - a composer with drafts and `$EDITOR`
  - context keymaps with `--dump-keys`
  - a status line, help, `:` commands and search
- Groups: a paged members view (`:members`), and a lazy refresh of a
  group's members and description when it is opened.
- Older messages on demand from the phone when scrolling past the oldest
  stored one, and the end of a chat's history.
- Replies: quotes of messages outside the loaded window, `gq` to jump to
  the quoted message, and sending replies (`R`).
- Reactions: shown below messages, and sent from a picker (`r`) or with
  `:react EMOJI`.
- Mentions: shown by the person's current name, mentions of this
  account marked, and completed from a group's members in the composer.
- Typing indicators and online status, shown in the chat header and sent
  behind `privacy.typing` and `privacy.presence`; tarcin is online only
  while the user is active.
- Media download: `o` opens and `s` saves an attachment, downloaded on
  demand within `media.max_download_mib` into a content-addressed cache
  capped at `media.cache_mib`; openers per MIME type; saving never
  replaces a file.
- Media send: `:attach PATH` with path completion; images, video, audio
  and documents, cached for opening and for sending again.
- Desktop notifications over D-Bus, focus-aware, one per chat, without
  muted chats or a backlog; the bell without a notification service;
  `[notifications]` settings.
- Disappearing messages: the chat's timer is shown, sent with each
  message and kept per message; expired messages and their media files
  are deleted. View-once media shows a placeholder.
- Message search: `:find` in all chats and `:findin` in the open chat,
  over a full-text index with Turkish-aware matching; a result opens its
  chat at the message.
- Editing own messages (`e`, within 20 minutes) and deleting messages for
  everyone (`dd`, own ones for two days, anyone's as a group admin).
- Forwarding (`f`): text, media without uploading it again, locations
  and contacts, marked forwarded, to a chat picked with the chat search.
- `]u` / `[u` unread navigation, and `:mute`, `:pin`, `:archive` with
  their opposites through WhatsApp's app state.
- `:delete-local`, and chats deleted or cleared on another device follow
  here, their cached media included.
- Copying a message's text (`y`, OSC 52) and reading it in `$PAGER` (`v`).
- `config.toml`: privacy settings, theme (`NO_COLOR` aware), media
  programs, notifications, log level and key bindings, with
  `tarcin config check`, `path` and `print-defaults`, and `--config`.
- `ui.theme = "high-contrast"`: no colours or faint text, with reversed
  selections and warnings.
- `--profile` for keeping accounts apart.
- `tarcin logout`, which unlinks the device and can delete the local
  data, and `tarcin backup FILE`, which saves the messages and settings,
  also while tarcin runs; the device's keys only with `--with-keys`.
- `tarcin --demo`: the UI running against simulated chats.
- `tarcin --version`, with link-time and VCS build metadata.
- A message store in SQLite with a single writer, migrations, full-text
  search, the LID as the canonical identity, and crash and full-disk
  tests that lose nothing.
- XDG directories with private modes, a check and fix for loose ones, and
  a single-instance lock per profile.
- A redacted log in `$XDG_STATE_HOME/tarcin/tarcin.log`, which also
  carries whatsmeow's lines.
- `:debug`: queues, caches, frames, job pools and goroutines;
  `--pprof ADDR` (loopback only) and `--trace FILE` for developers.
- Man pages `tarcin(1)` and `tarcin(5)`, shell completions for bash, zsh
  and fish, and a desktop file; `make install` installs them.
- AUR packages `tarcin` and `tarcin-git`, checked with `make aur-check`,
  and a reproducibility check, `make repro-check`, that `make release`
  runs.
- Storage and load benchmarks for the performance budgets
  (`make storagebench`, `make loadtest`).
- `:new NAME` starts a chat with a contact of your address book, and
  `:new +NUMBER` with a phone number, which is checked with WhatsApp
  first; `tab` completes contact names. A chat that exists just opens.

### Changed

- `notifications.when = "other-chats"` notifies while the terminal has
  focus too, for every chat but the open one. The default, `"unfocused"`,
  keeps notifying only while the terminal does not have focus.

### Fixed

- `sudo make install` no longer builds as root, which left root-owned
  files in `./build`: `make` builds everything it installs, as the user,
  and `make install` only copies.
- The choices of a `tab` completion (`:attach` paths) show beside the
  command line; they stayed hidden until it closed.
- The open chat no longer marks new messages read, or sends read
  receipts, while the terminal does not have focus; it is read when you
  come back.
- Pins, mutes, archives and other settings shared with the phone no
  longer stop syncing after an app state patch fails to verify: the
  affected collection is synced again in full.

### Security

- Terminal-safe rendering of all untrusted text, fuzzed together with
  message normalisation, history sync and config parsing.
- A message from another sender with the ID of a stored message no
  longer changes it: someone could fill the placeholder of a view-once
  message and make it read as its sender's, or as your own.
- Names people choose for themselves (push and business names) show
  with a leading "~", so they cannot pass for a contact, "You" or a
  phone number; names that show nothing fall back to the number.
- A file of an unknown type is not handed to the default opener, which
  would guess its type from its content and could open a page in the
  browser; press `s` to save it.
- Saving a file with a very long extension no longer crashes tarcin,
  and saved names never start with a dot or a dash.
- A group member can no longer change the group's disappearing-message
  timer through a message; direct chats accept only WhatsApp's timers.
- Mentions are capped at 1024 per message and create no identities;
  waiting edits, revokes and receipts are stored once, capped per chat
  and always pruned; reactions to messages that never arrive are pruned.
- A read-on-another-device receipt is accepted only from this account.
- A sender's own times for edits and reactions are never later than the
  server's.
- Names inserted by mention completion are sanitized, and joiners no
  longer lift the cap on stacked combining marks.
- Panic values are kept out of the log and of job errors, since they
  may hold message content.
- The bump-whatsmeow workflow runs the new code with a read-only token;
  the job that pushes runs no module code.
- Edits and deletions are applied only when their sender may make them:
  the author, or a group admin for an admin deletion.
- History sync notifications are accepted only from this account's own
  devices.
- golang.org/x/net is raised to v0.60.0 for the vulnerabilities
  govulncheck reports in older versions.
