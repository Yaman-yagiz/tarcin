# Architecture overview

tarcin is a single-binary, single-user terminal client for WhatsApp. It
targets Arch Linux and works on any modern Linux. This page is the entry
point to the architecture. Each detailed document covers one concern.

| Document | Covers |
|---|---|
| [event-pipeline.md](event-pipeline.md) | Concurrency, delivery guarantees, backpressure, lifecycle |
| [data-model.md](data-model.md) | SQLite schema, identity (LID/PN) model, queries, migrations |
| [ui.md](ui.md) | Bubble Tea architecture, virtualised lists, input, safe rendering |
| [security.md](security.md) | Threat model and controls |
| [performance.md](performance.md) | Budgets, scenarios, instrumentation, regression control |
| [packaging.md](packaging.md) | Build, Arch/AUR packaging, XDG paths, releases |

Decisions are recorded in [ADRs](../adr/README.md).

## 1. Goals

1. **Responsive with large and busy groups.** Input and scrolling never
   wait on the network, the disk or group size. Existing terminal
   clients freeze in such groups; this is the problem that motivated the
   project.
2. **No lost or duplicated messages.** A message is persisted before it is
   acknowledged, and replays are idempotent.
3. **Private by default.** Strict file permissions, no telemetry, no
   content in logs, and terminal-safe rendering of untrusted text.
4. **Native to Arch.** XDG paths, Arch Go packaging flags, an AUR
   package, man pages, and no extra services.
5. **Easy to use.** Works on first launch, comes with sensible vim-style
   defaults, and has discoverable help.
6. **Maintainable open source.** GPL-3.0-or-later, documented decisions,
   tests and benchmarks, and a small set of dependencies.

### Non-goals (for now)

- Voice or video calls.
- Multi-protocol support (Telegram, Signal and so on). We stay
  WhatsApp-specific and avoid nchat's lowest-common-denominator trap.
- Automation or bot features: bulk sending, scheduled messages,
  scraping.
- Running as a daemon or server, or exposing any network API.
- Platforms other than Linux. They may work, but they are not
  supported.

## 2. Quality priorities

When goals conflict, the higher one wins.

1. **Correctness of data**: no loss, no corruption, correct ordering
2. **Security and privacy**
3. **Responsiveness**: the UI first, then throughput
4. **Simplicity**: of operation, of code, and of dependencies
5. **Features**

## 3. Constraints

| Constraint | Consequence |
|---|---|
| There is no official API for personal accounts | We use the unofficial multi-device protocol via whatsmeow, and accept ToS and account risk, disclosed to users |
| whatsmeow has no semver releases and churns its protocol | Pin a pseudo-version, run a scheduled bump PR, release monthly |
| whatsmeow dispatches events synchronously | Handlers must be fast and non-blocking towards the UI |
| SQLite allows one writer | One explicit writer goroutine |
| Terminals differ (keyboard protocol, grapheme width, images) | Feature-detect, fall back gracefully, document the supported terminals |
| GPL-3.0-or-later | Every dependency must be GPL-3.0-compatible. A CI license check enforces it |

## 4. System context

```text
   ┌──────────────┐  multi-device protocol (Noise + Signal)  ┌──────────────────┐
   │   tarcin     │◄────────────────────────────────────────►│ WhatsApp servers │
   │ (terminal)   │                                          └────────┬─────────┘
   └──┬───┬───┬───┘                                                   │
      │   │   │                                              ┌────────▼─────────┐
      │   │   └─ D-Bus: org.freedesktop.Notifications        │ user's phone     │
      │   │                                                  │ (primary device) │
      │   └───── exec: $EDITOR, $PAGER, xdg-open, openers    └──────────────────┘
      │
      └───────── files: XDG config / data / state / cache
```

tarcin is a **linked companion device**. The user's phone stays the
primary device: it pairs tarcin and provides the history sync.

## 5. Building blocks

```text
cmd/tarcin/                entry point: flags, subcommands (login, logout, backup, config, version)
internal/
  app/                     composition root, lifecycle, signals, single-instance lock
  buildinfo/               version metadata
  config/                  TOML config, defaults, validation (ui/keymap parses the bindings)
  paths/                   XDG resolution, permission checks
  domain/                  core types and events (no internal deps)
  backend/                 Backend port (interface) + backend/fake for tests and demo mode
  wa/                      whatsmeow adapter implementing backend.Backend
  store/                   SQLite driver registration, migrations, Writer, read queries
  service/                 use cases: open chat, send, mark read, search, logout
  jobs/                    bounded worker pool, singleflight, rate limiting
  ui/                      Bubble Tea root model, Bridge
    chatlist/ msglist/ composer/ statusbar/ overlay/ keymap/ theme/
  sanitize/                terminal-safe text
  textfold/                search normalisation (Turkish-aware folding)
  media/                   media cache, download, open
  notify/                  D-Bus notifications, bell fallback
  logging/                 slog setup and redaction
  devtools/gen/            deterministic synthetic data generator
docs/                      user guide, architecture, ADRs, release notes, man pages
packaging/                 AUR PKGBUILDs, desktop file, completions
```

### Dependency rules

The rules are enforced by `internal/archtest`, which parses the imports
of every non-test file. A rule change means updating that test and this
list together.

- `domain` and `paths` import only the standard library.
- `ui` depends on `service` interfaces and `domain`. It **never** imports
  `wa`, `store`, whatsmeow or the SQLite driver.
- `wa` depends on `domain`, plus a narrow ingest interface,
  `backend.Sink`, that `store.Writer` implements. It never imports
  `store`, `ui`, `app` or `paths`: `app` opens `session.db` with the
  store's driver and hands it over as a `*sql.DB`.
- `store` depends on `domain`, the `backend` port and leaf helpers such
  as `textfold`. It never imports `ui`, `app`, `wa`, `service` or
  `jobs`; it reports commits through a callback that `app` connects to
  the bridge.
- Only `app` imports `ui`, and only `cmd/tarcin` imports `app`. `app`
  wires everything together.
- whatsmeow (`go.mau.fi/whatsmeow`) is imported only by `wa`. Its types
  are converted to `domain` types at the boundary. This keeps a future
  protocol-library switch possible
  ([ADR-0003](../adr/0003-whatsmeow-protocol-library.md)).
- The SQLite driver is imported only by `store`.
- Bubble Tea (`charm.land/…`) is imported only by `ui` and `app`.

## 6. Runtime view (summary)

1. **Startup:** paths → lock → DBs and migrations → UI from cache →
   background connect. The UI is interactive before the network is.
2. **Incoming message:** whatsmeow handler → normalise → writer commit →
   receipt → bridge (coalesced) → UI batch → `View` of the visible rows.
3. **Outgoing message:** UI command → pending row → per-chat sender →
   sent or failed → UI update.
4. **Opening a chat:** `Enter` → read-pool query for the newest page →
   window shown. Older pages load on scroll. Group metadata is refreshed
   lazily if it is stale.

The guarantees and the details are in
[event-pipeline.md](event-pipeline.md).

## 7. Cross-cutting concerns

- **Configuration:** TOML at `$XDG_CONFIG_HOME/tarcin/config.toml`.
  tarcin validates it at startup with precise error messages and never
  rewrites it. `tarcin config print-defaults` prints a complete,
  commented default config.
- **Errors:** wrapped with context (`fmt.Errorf("…: %w", err)`). They are
  shown in the status line with a short message and logged with details.
  Fatal errors restore the terminal before exiting.
- **Contexts:** every blocking operation takes a `context.Context` with a
  deadline. The root context is cancelled on SIGINT, SIGTERM or SIGHUP.
- **Logging:** `log/slog` with redaction (see [security](security.md) §5).
- **Internationalisation:** the UI strings are English in v1, kept in one
  place so that a translation layer can be added later. Turkish-aware
  search folding is built in.
- **Testing:**
  - unit tests per package
  - fake backend for pipeline and UI tests
  - property tests for idempotency
  - fuzzing of the sanitiser and the parsers
  - golden snapshots of the UI
  - benchmarks with budgets ([performance](performance.md))

## 8. Glossary

| Term | Meaning |
|---|---|
| JID | WhatsApp address, e.g. `123@s.whatsapp.net`, `123@lid`, `123-456@g.us` |
| PN | Phone-number JID |
| LID | Linked identity, an opaque per-user identifier that WhatsApp now prefers over PNs |
| History sync | Bulk transfer of past conversations from the phone to a linked device |
| App state | Synced settings: contacts, pins, mutes, archives |
| Receipt | Delivery or read acknowledgement |
| Window | The slice of a chat's messages held in memory around the viewport |
| Anchor | The scroll position as (message, line offset), independent of absolute line numbers |
