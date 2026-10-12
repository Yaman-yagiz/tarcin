# Architecture Decision Records

The format and the rules are described in
[ADR-0001](0001-record-architecture-decisions.md). New ADRs start from
the [template](template.md).

| ADR | Title | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-go-as-implementation-language.md) | Use Go as the implementation language | Accepted |
| [0003](0003-whatsmeow-protocol-library.md) | Use whatsmeow for the WhatsApp protocol, behind a Backend port | Accepted |
| [0004](0004-bubble-tea-tui.md) | Use Bubble Tea v2 with a custom virtualised message list | Accepted |
| [0005](0005-sqlite-storage.md) | Use SQLite (mattn/go-sqlite3) with separate session and message databases | Accepted |
| [0006](0006-in-process-event-pipeline.md) | Use an in-process event pipeline; no message broker | Accepted |
| [0007](0007-license-gpl-3.0-or-later.md) | License tarcin under GPL-3.0-or-later | Accepted |
| [0008](0008-xdg-paths-and-file-permissions.md) | Follow the XDG Base Directory spec, with strict permissions | Accepted |
| [0009](0009-lid-canonical-identity.md) | Use the LID as the canonical user identity, with PN aliases | Accepted |
| [0010](0010-encryption-at-rest-deferred.md) | Rely on full-disk encryption in v1; defer app-level encryption at rest | Accepted |
| [0011](0011-project-name.md) | Name the project "tarcin" | Accepted |
| [0012](0012-config-format-and-cli.md) | TOML configuration with BurntSushi/toml; stdlib `flag` CLI with subcommands | Accepted |
