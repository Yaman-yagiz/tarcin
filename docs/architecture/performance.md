# Performance plan

tarcin exists because existing terminal clients stall on large and
busy groups. This document defines how we measure responsiveness, the
budgets we hold ourselves to, and how regressions are caught.

> The budgets in §3 are release gates, set from measurements on the
> reference machine.

## 1. Principles

1. **Measure before optimising.** Every performance claim in a PR needs
   a benchmark, a profile or a trace.
2. **Work is proportional to what is visible, not to what is stored.**
   Opening a chat, scrolling and rendering must cost O(visible rows).
   They must not grow with history size or group size.
3. **The UI never waits on I/O.** The network and the disk only reach
   the UI as messages (see [event-pipeline](event-pipeline.md)).
4. **Memory is bounded by design.** Every queue and every cache has a
   documented capacity and a documented overflow behaviour.
5. **Idle means idle.** With no input and no events, CPU use stays at
   about 0%. There are no busy timers.

## 2. Reference environment

Recorded with every benchmark run:

- CPU model, core count, RAM, disk type (NVMe or SATA SSD), kernel and Go
  versions.
- The terminal emulator and its size: we measure at 80×24 and 200×50.
- The dataset (see §4).

## 3. Budgets

The budgets were set from measurements on the reference machine: an
Intel i5-13400F with 16 GiB of RAM and an NVMe SSD, Linux 6.18 and
Go 1.27. The *Measured* column holds the latest results there.
`make storagebench` and `make loadtest` reproduce them.

| ID | Metric | Budget | Measured | How measured |
|---|---|---|---|---|
| B1 | Cold start until the cached chat list is visible (1,000 chats) | ≤ 300 ms p95 | 47 ms p50, about 100 ms p95 | `make storagebench`: open, load every chat and name, draw the first frame |
| B2 | Opening a chat until the first page is visible (100k-message DB, 10k in that chat) | ≤ 50 ms p95 | 0.9 ms p95 | `make storagebench`: the UI command plus the DB query |
| B3a | Key press → frame ready (`Update` + `View`), idle backend | ≤ 2 ms p95 | 0.83 ms | Load harness, *ready* |
| B3b | Key press → frame written to the terminal, idle backend, 60 fps | ≤ 25 ms p95 | 22.0 ms | Load harness, *flushed* |
| B4 | Key press → frame written while ingesting 100 msg/s into a 5,000-member group | ≤ 33 ms p95, and ready ≤ 2 ms p95 | 22.2 ms, ready 1.03 ms; at most 23.0 ms, ready 1.21 ms, under S4, S6, S7 and S8 | Load harness (scenarios S4–S8) |
| B5 | Scrolling one page in a 10k-message chat | ≤ 16 ms p95, with no allocations growing with history size | 0.03–0.04 ms per page, with constant allocations (10k vs 100k) | `BenchmarkScroll`, allocation tests |
| B6 | Sustained ingest throughput (history sync) | ≥ 5,000 msg/s on SSD | 16,538 msg/s at 100k messages, 10,251 at 300k | `make storagebench`: the writer and applier, chunks of 500 |
| B7 | Resident memory with a 100k-message DB and a 5k-member group open | ≤ 150 MB RSS, flat as the DB grows | 30 MB at 100k messages, 34 MB at 300k | `make storagebench`: `/proc/self/status` of a separate process |
| B8 | Idle CPU after 60 s without input or events | ≤ 1.5% of one core | ≈ 1.0% | Load harness idle phase; `pidstat` on the binary |
| B9 | Event queue | 0 dropped critical events; queue wait ≤ 100 ms p99 under load | 0 dropped; longest wait 2.2 ms under S5, 49.9 ms during the S6 history import | Writer statistics in the load harness; S10 and S11 tests |
| B10 | Reconnect after a network flap | UI stays interactive; reconnect starts ≤ 2 s after the network returns | The UI stays interactive under S8 with the fake backend: key → ready 1.03 ms p95; the reconnect time is measured on a real account | Load harness (scenario S8); a real network for the reconnect time |

Why B3 and B8 changed:

- **B3.** Bubble Tea writes frames only on its frame ticker. Every key
  therefore waits for the next tick: up to 16.7 ms at 60 fps, plus at
  most one more tick when the tick races with `View`. A budget of 16 ms
  for the written frame is below that floor. B3 is split into what
  tarcin controls (B3a, under 1 ms in practice) and what the user sees
  (B3b).
- **B8.** The same 60 Hz ticker wakes the process even when nothing
  changes. That costs about 1% of one core on the reference machine,
  and is the price of B3b. A lower frame rate would trade key latency for
  idle CPU. If users ask for it, it becomes a configuration option.

## 4. Datasets and scenarios

A generator (`internal/devtools/gen`) creates deterministic
synthetic data. It is seeded and requires no real account.

| Scenario | Description |
|---|---|
| S1 | 10,000 messages in total across 50 chats |
| S2 | 100,000 messages; one chat holds 10k |
| S3 | 1,000,000 messages (stress test only, not a gate) |
| S4 | A group with 5,000 members, long display names, emoji and RTL names |
| S5 | A burst of 100 msg/s for 60 s into the open chat, plus 20 msg/s spread over other chats |
| S6 | A history-sync style bulk import of 50k messages while the user navigates |
| S7 | Media downloads in progress while switching chats |
| S8 | Network flaps: disconnect and reconnect every 10 s for 2 minutes |
| S9 | Duplicate delivery: every event delivered twice, in shuffled order |
| S10 | Crash safety: kill -9 during a write burst, then restart and verify integrity |
| S11 | Slow disk (an injected fsync delay) and a full disk (ENOSPC) |
| S12 | Hostile content: 1 MB messages, control and escape sequences, zero-width and bidi characters |

## 5. Instrumentation

- `--pprof=127.0.0.1:6060` turns on `net/http/pprof`. It is **off by
  default** and only binds to loopback.
- `--trace=FILE` records a `runtime/trace` into a new file (never an
  existing one) with named regions: `ingest`, `db.write`, `db.read`,
  `ui.update`, `ui.view`. Both live in `internal/diag`.
- Internal counters are exposed in a debug pane (`:debug`); the
  writer's and the job pools' counters are also logged on exit:
  - queue depth, enqueue wait time, batch size, transaction duration
  - render-cache hit rate, frames rendered, frames skipped
  - goroutine count
- These counters contain no message content (see [security](security.md)).

## 6. Regression control

- `go test -bench` suites live next to the code they measure, mainly the
  message list, render cache, store queries and writer.
- CI runs the benchmarks on every PR and compares them with `benchstat`
  against `main`. CI hardware is noisy, so CI only **warns** on
  regressions above 10%. Budgets are enforced on the reference machine
  before each release, and the *Measured* column above is updated.
- `go test -race` runs on every PR.
- Allocation counts for `View` and the scroll path are asserted in tests
  (`testing.AllocsPerRun`), so that O(history) allocations fail
  immediately.
