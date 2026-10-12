# ADR-0012: TOML configuration with BurntSushi/toml; stdlib `flag` CLI with subcommands

- **Status:** Accepted
- **Date:** 2026-10-11
- **Deciders:** @Yaman-yagiz
- **Related:** [overview](../architecture/overview.md) §7,
  [packaging](../architecture/packaging.md) §2,
  [ADR-0008](0008-xdg-paths-and-file-permissions.md)

## Context

tarcin needs a user-editable configuration file for keybindings, theme,
privacy toggles and openers, and a small command-line interface. The
command line has to cover the TUI itself, `login`, `logout`, `backup`,
`config` and `version`, plus a few global flags such as `--profile`,
`--config`, `--demo`, `--pprof` and `--trace`.

Requirements:

- **Strict validation.** An unknown key or a wrong type is reported with
  the file, line and key path. Typos must never be silently ignored.
- **Read-only.** The config is never rewritten. `tarcin config
  print-defaults` prints a complete, commented example instead.
- **Few dependencies.** Every dependency must be GPL-3.0-compatible and
  maintained ([ADR-0007](0007-license-gpl-3.0-or-later.md)).
- **Familiar to Arch users.** TOML is already used by iamb, alacritty
  and pacman-adjacent tools.

The two candidate TOML libraries, as of 2026-10-11:

| Library | Version | License | Dependencies | Strictness | Error positions |
|---|---|---|---|---|---|
| [BurntSushi/toml](https://github.com/BurntSushi/toml) | v1.6.0 (2025-12-18) | MIT | none | `MetaData.Undecoded()` lists unknown keys | `ParseError.ErrorWithPosition()` with line, column and context |
| [pelletier/go-toml/v2](https://github.com/pelletier/go-toml) | v2.4.3 (2026-07-05) | MIT | none | `Decoder.DisallowUnknownFields()` | `DecodeError` with position |

Both would work. Decoding speed is irrelevant for a small config file.

## Decision

- **Config format:** TOML, decoded with **`github.com/BurntSushi/toml`**.
  - The file is `$XDG_CONFIG_HOME/tarcin/config.toml`.
  - Unknown keys are reported as errors through `MetaData.Undecoded()`,
    and every error names the file, the line and the key path.
  - Defaults live in Go code and are documented by
    `tarcin config print-defaults`.
  - The config may contain no secrets
    ([security](../architecture/security.md) §3).
- **Command line:** the standard library `flag` package with a small
  subcommand dispatcher in `cmd/tarcin`. There is no CLI framework.
  - `tarcin [global flags]` starts the TUI.
  - `tarcin <command> [flags]`, where the command is one of `login`,
    `logout`, `backup`, `config` (`check`, `path`, `print-defaults`) and
    `version`.
  - Flags are accepted as both `-flag` and `--flag`. Short-flag bundling
    (`-abc`) is not supported, and none is needed.
  - Shell completions are written by hand
    ([packaging](../architecture/packaging.md) §2).

## Alternatives considered

| Option | Why not chosen |
|---|---|
| pelletier/go-toml/v2 | Equally suitable. BurntSushi has the larger user base and an `Undecoded()` API that gives precise unknown-key reports. Either could be swapped behind `internal/config`. |
| YAML | Significant whitespace and implicit typing (the "Norway problem") cause confusing config errors |
| JSON | No comments, poor for hand-editing |
| A custom INI-style format (nchat, whatscli) | Ad-hoc parsing and weak typing |
| spf13/cobra or urfave/cli | Adds dependencies and generated boilerplate for about six commands |

## Consequences

- One small, dependency-free library is added for `internal/config`.
- Keeping the CLI surface small is a deliberate constraint. If it ever
  grows past what `flag` handles cleanly, a new ADR revisits this
  decision.

## Revisit when

- BurntSushi/toml becomes unmaintained or changes its license.
- The CLI needs features that `flag` cannot provide, such as
  short-option bundling or nested command trees.
