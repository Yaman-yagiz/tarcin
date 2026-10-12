# ADR-0004: Use Bubble Tea v2 with a custom virtualised message list

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [UI architecture](../architecture/ui.md),
  [Bubble Tea](https://github.com/charmbracelet/bubbletea)

## Context

whatscli froze because many goroutines mutated tview widgets and
triggered redraws, and because it rendered the whole history as one
string. We need a framework that gives one owner of UI state and supports
modern terminal input (the kitty keyboard protocol, bracketed paste, OSC
52, synchronised output). It must also let us render only what is
visible.

The candidates' status as of 2026-10:

- **Bubble Tea v2.1.0** (MIT). v2 has been stable since 2026-02 and is
  very active.
- **tview.** Its last tag is from 2025-08, and it is stuck on tcell v2.8.1.
- **vaxis** (Apache-2.0). Pre-1.0, essentially one maintainer, but it has
  built-in image protocols.

## Decision

We will use **Bubble Tea v2**, **Lip Gloss v2** and the **Bubbles v2
`textarea`**. We will write our own virtualised message list and chat
list (`internal/ui/msglist`, `internal/ui/chatlist`) instead of using the
Bubbles `viewport` or `list`. Those components do O(total) work per frame
or lag at about 8k items.

## Alternatives considered

| Option | Pros | Cons | Why not chosen |
|---|---|---|---|
| vaxis | Kitty, sixel and OSC 66 support; lazy list builders | Pre-1.0, a single maintainer, a changing API | Kept in reserve if inline images become a v1 requirement |
| tview + tcell | Mature widgets | Stale tcell, stateful widgets (the whatscli failure mode), no images | Rejected |
| mauview | Used by gomuks | Low activity, "work in progress" | Rejected |

## Consequences

- One `Update` loop owns the state, and every I/O runs in a `tea.Cmd`.
- We own and must benchmark the list components. The rendering cost per
  frame in v2 is higher than in v1 (#1724, #1481), so coalescing and a
  cheap `View` are mandatory.
- There are no inline images in v1. Media opens externally, and kitty or
  half-block previews are explored later.
- `ultraviolet` is untagged and teatest v2 is experimental. We pin exact
  versions and keep most UI tests as plain `Update`/`View` tests.

## Revisit when

- The UI benchmarks miss the B3–B5 budgets in
  [performance](../architecture/performance.md) after reasonable tuning.
  The fallback is vaxis.
- Inline media previews become a release requirement.
