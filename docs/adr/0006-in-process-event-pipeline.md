# ADR-0006: Use an in-process event pipeline; no message broker

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [event pipeline](../architecture/event-pipeline.md),
  [whatscli](https://github.com/normen/whatscli)

## Context

The initial brief asked whether Kafka or RabbitMQ should be used to
absorb bursts from busy groups. The workload looks like this:

- **One user and one process.** Peak inbound traffic is hundreds of
  messages per second during a history sync, and far less when the
  client is live.
- The whatscli freezes came from work done on the UI and event
  goroutines, not from a lack of queue capacity.
- A broker adds a service to install and run, uses hundreds of MB of RAM,
  and brings its own failure modes. It also breaks the goal of a single
  binary with no services.

Durability is already provided by persisting to SQLite before sending
the receipt. A redelivery is decryptable thanks to whatsmeow's
decrypted-event buffer.

## Decision

We will not use an external broker. Events flow through bounded Go
channels into a single SQLite writer, and are coalesced towards the UI,
as specified in [event pipeline](../architecture/event-pipeline.md).
Background jobs run in a bounded in-memory worker pool and are not
persisted in the MVP, because every job can be derived again from the
state of the DB.

## Alternatives considered

| Option | Why not chosen |
|---|---|
| Kafka | Built for high-volume, replayable streams with several consumer groups. We have one consumer. |
| RabbitMQ | Built for distributing work across independent services. We have one process. |
| Unbounded channels or a goroutine per event | Unbounded memory, no backpressure, no ordering (the whatscli pattern) |
| A durable `jobs` table from the start | Speculative. It adds schema and retry logic before any need is shown. |

## Consequences

- There is nothing extra to install or run, and the memory footprint is
  bounded and small.
- Backpressure is explicit. A full live queue slows whatsmeow's decryption
  loop instead of dropping data.
- A durable job queue, if one is ever needed, will be a SQLite table, not
  a broker.

## Revisit when

- A job turns out to need durable retry across restarts. Then add a
  `jobs` table.
- tarcin is split into several processes, for example a daemon plus
  multiple UIs. Even then, a local socket comes before a broker.
