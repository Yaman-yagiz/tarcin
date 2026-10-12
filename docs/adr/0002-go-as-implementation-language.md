# ADR-0002: Use Go as the implementation language

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [ADR-0003](0003-whatsmeow-protocol-library.md)

## Context

The client must be fast, ship as a single easy-to-install binary on Arch,
and talk to the WhatsApp multi-device protocol. The most mature protocol
implementation is whatsmeow, which is written in Go. Clients that reach
whatsmeow from another language through FFI or cgo bridges (nchat in C++,
wptui in Rust) have recurring memory-ownership bugs, and nchat has to
maintain a patch on top of whatsmeow.

## Decision

We will write tarcin entirely in Go, with the `go` directive at 1.26,
and call whatsmeow directly in-process.

## Alternatives considered

| Option | Pros | Cons | Why not chosen |
|---|---|---|---|
| Rust + whatsapp-rust | Memory safety, Ratatui | whatsapp-rust is younger (v0.7) and less battle-tested than whatsmeow | Protocol risk is our biggest risk; we pick the most proven library |
| Rust or C++ UI + whatsmeow via FFI | A UI framework of choice | FFI leaks (wptui #392…), double toolchain, harder packaging | Complexity without user benefit |
| TypeScript + Baileys | Large ecosystem | Needs the Node runtime; higher memory use | Conflicts with the low-resource, single-binary goal |

## Consequences

- One toolchain. Arch's Go packaging rules apply directly.
- Goroutines and `context` make it straightforward to implement the
  concurrency design.
- The GC and goroutine model need care on hot paths (rendering,
  ingest). This is covered by benchmarks and allocation assertions
  ([performance](../architecture/performance.md)).
- cgo is still required for SQLite ([ADR-0005](0005-sqlite-storage.md)).

## Revisit when

- whatsmeow is abandoned and the best-maintained protocol library is in
  another language.
