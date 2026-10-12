# ADR-0001: Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [ADR index](README.md), [template](template.md)

## Context

tarcin depends on an unofficial protocol library, an evolving TUI
framework and a local database whose trade-offs are easy to forget. New
contributors and future maintainers need to know *why* a choice was made,
what evidence supported it, and when it should be revisited.

## Decision

We will record every significant architectural or project-level decision
as an Architecture Decision Record (ADR) in `docs/adr/`, following the
[template](template.md).

- ADRs are numbered sequentially (`NNNN-kebab-case-title.md`) and never
  renumbered.
- An accepted ADR is immutable except for its status line and small
  clarifications. To change a decision, write a new ADR that supersedes it
  and update the old ADR's status.
- Each ADR links to the evidence it relies on and lists concrete
  "revisit when" triggers.
- A pull request that changes a recorded decision must include the new or
  updated ADR.

## Alternatives considered

| Option | Why not chosen |
|--------|----------------|
| Decisions only in issues or PR threads | Hard to find; context gets lost when discussions are long. |
| One large design document | Mixes stable decisions with volatile plans; changes are hard to review. |

## Consequences

- Decisions get slightly slower to make, but much easier to review and revisit.
- `docs/adr/README.md` must be kept in sync with the ADR files.

## Revisit when

- The ADR count or format becomes a burden (for example, if we need tooling
  such as `adr-tools` or log4brains).
