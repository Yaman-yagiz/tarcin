# ADR-0005: Use SQLite (mattn/go-sqlite3) with separate session and message databases

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [data model](../architecture/data-model.md),
  [whatsmeow's SQL store](https://pkg.go.dev/go.mau.fi/whatsmeow/store/sqlstore)

## Context

We need persistent, indexed and searchable local storage for 100k+
messages, with atomic writes and no server process. whatsmeow's session
store needs a `database/sql` SQLite driver with foreign keys enabled.
The three viable drivers are mattn (cgo), modernc (pure Go) and ncruces
(wasm2go). They perform within about 2× of each other; the driver will
not be the bottleneck.

## Decision

- **Engine:** SQLite in WAL mode, with `synchronous=NORMAL` and
  `foreign_keys=ON`. Writes use `BEGIN IMMEDIATE`, through one writer
  connection and a read-only pool. The full settings are in the
  [data model](../architecture/data-model.md#5-connections).
- **Driver:** `github.com/mattn/go-sqlite3`, registered under our own name
  `sqlite3-tarcin` with a connect hook. The Arch package builds with
  `-tags libsqlite3`, so it links `core/sqlite` and receives SQLite
  security fixes through pacman. Other builds use the bundled amalgamation
  with `sqlite_fts5`.
- **Two files per profile:** `session.db` (whatsmeow) and `tarcin.db`
  (ours).
- **Search:** an FTS5 contentless-delete table, filled with text folded
  in Go (`ı`/`İ` → `i`, then `unicode61 remove_diacritics 2`).

## Alternatives considered

| Option | Why not chosen |
|---|---|
| modernc.org/sqlite | It would allow `CGO_ENABLED=0`, but it is slower, needs its libc version pinned exactly, and cannot use the system SQLite. **It is the fallback** if cgo becomes a problem (for example for a `-bin` cross-build). |
| ncruces/go-sqlite3 | Pre-1.0, more memory per connection, FTS5 is opt-in. Worth reconsidering for its encrypting VFS ([ADR-0010](0010-encryption-at-rest-deferred.md)). |
| One shared database | Session writes would contend with bulk history inserts, and logout would have to delete selectively |
| PostgreSQL | Needs a server |
| bbolt or other key-value stores | No query planner or FTS; leads to ad-hoc indexes (the legacy gomuks lesson) |
| Kafka or RabbitMQ as storage or a queue | See [ADR-0006](0006-in-process-event-pipeline.md) |

## Consequences

- cgo is required, and `gcc` is a build dependency (it is part of
  `base-devel` on Arch).
- mattn and ncruces must never both be linked: both register
  `sqlite3`, and the duplicate registration panics.
- Behind our `store` package the driver can be swapped. Swapping it means
  changing the connect hook and DSN, not the queries.

## Revisit when

- cgo blocks a distribution channel we need.
- App-level encryption is accepted and requires a different driver.
- The storage benchmarks show the driver on a hot path.
