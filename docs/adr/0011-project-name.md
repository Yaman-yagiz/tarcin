# ADR-0011: Name the project "tarcin"

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [WhatsApp brand guidelines](https://www.meta.com/brand/resources/whatsapp/whatsapp-brand/)

## Context

The working directory was called `whatsappcli`. WhatsApp's brand
guidelines forbid using the mark, or any "variation, phonetic equivalent,
takeoff or abbreviation" of it, in the name of a third-party product,
domain or username. The names `whatscli`, `whatsappcli` and arguably `wa…`
are therefore at risk of takedown. An AUR package, a GitHub repository and
a Go module path are all hard to rename once users depend on them.

## Decision

The project, binary, Go module and AUR packages are named **`tarcin`**
("tarçın", cinnamon), written in ASCII.

- On 2026-10-11 the name was free in the AUR, in the official
  repositories, and under `github.com/Yaman-yagiz`.
- The description may contain the factual phrase "unofficial terminal
  client for WhatsApp", together with a non-affiliation notice. WhatsApp
  logos are never used.

## Alternatives considered

| Option | Why not chosen |
|---|---|
| `whatsappcli`, `whatscli`, `wacli` | Trademark risk; `whatscli` and `wacli` are also taken |
| `sohbet`, `natter` | Both were available. The maintainer preferred `tarcin`. |

## Consequences

- The module path is `github.com/Yaman-yagiz/tarcin`.
- The local working directory may keep its old name. Only published names
  matter.

## Revisit when

- A naming conflict or a trademark complaint arises.
