# Contributing to tarcin

Thank you for your interest! tarcin is in early development. Please
read the [architecture overview](docs/architecture/overview.md) before
you start working on a change.

Everyone taking part follows the [code of conduct](CODE_OF_CONDUCT.md).

## Your first contribution

1. **Try it.** `make build && ./build/tarcin --demo` runs tarcin on
   simulated chats, with no account or network.
2. **Pick something small.** Issues labelled
   [`good first issue`](https://github.com/Yaman-yagiz/tarcin/labels/good%20first%20issue)
   are well described and touch little code. Say in the issue that you
   are working on it.
3. **Find your way.** The [architecture overview](docs/architecture/overview.md)
   explains the parts:

   | Directory | What it is |
   |---|---|
   | `internal/ui` | The terminal interface (Bubble Tea) and its panes |
   | `internal/app` | Wires the UI, the store and WhatsApp together |
   | `internal/store` | The SQLite message database and its single writer |
   | `internal/wa` | The WhatsApp connection (whatsmeow) |
   | `internal/backend/fake` | A simulated account for tests and the demo |
   | `internal/sanitize` | Makes untrusted text safe for the terminal |
   | `docs/` | The user guide, architecture and decisions |

4. **Test it.** Write a test next to the code you change, using the fake
   backend, and run `make ci`. A UI change may update a golden snapshot:
   check the diff, then `make golden`.
5. **Send it.** One change per pull request, with a
   [Conventional Commits](https://www.conventionalcommits.org/) message
   and a sign-off (`git commit -s`). The template asks a few questions;
   answer what applies.

Questions are welcome in the issue you work on.

## Ground rules

- **Discuss larger changes first.** Open an issue that describes the
  problem before you send a large PR.
- **Decisions are documented.** A change that alters an architectural
  decision needs a new or updated [ADR](docs/adr/README.md).
- **Performance is a feature.** Changes to the UI, ingest or storage hot
  paths must include benchmark results (see
  [performance](docs/architecture/performance.md)).
- **Privacy is a feature.** Never log message content, keys or full
  phone numbers. Never add telemetry, network listeners or automation
  features.
- **No automation or bulk-messaging features.** They will not be accepted
  (see [security](docs/architecture/security.md) §7).

## Development setup (Arch Linux)

```sh
sudo pacman -S --needed go base-devel git sqlite
```

The Makefile passes `-tags sqlite_fts5` (bundled SQLite with full-text
search). If you run `go` directly, pass it yourself, or set it once with
`go env -w GOFLAGS=-tags=sqlite_fts5`.

```sh
make build   # build ./build/tarcin
make test    # go test -race ./...
make lint    # formatting and static analysis
make vuln    # govulncheck
make licenses  # dependency license compatibility
make ci      # every CI check, natively
make ci-container  # every CI check in the archlinux:base-devel CI image
make help    # list all targets
```

Run `make ci` (or `make ci-container`) before opening a pull request.

The Makefile never overrides `GOFLAGS`, `CGO_*FLAGS` or `LDFLAGS` from
your environment.

## Code conventions

- Format with `gofmt`, and keep the code free of `go vet` and linter
  warnings.
- Every Go file starts with
  `// SPDX-License-Identifier: GPL-3.0-or-later`.
- Follow the dependency rules in
  [overview §5](docs/architecture/overview.md#5-building-blocks).
- No I/O in Bubble Tea `Update` or `View`. No network calls in whatsmeow
  event handlers.
- Wrap errors with context (`fmt.Errorf("doing x: %w", err)`). Every
  blocking call takes a `context.Context`.
- Write tests next to the code. Use the fake backend
  (`internal/backend/fake`) rather than a real account.
- UI changes that alter a screen fail the golden snapshots in
  `internal/ui/testdata/golden/`. Review the change, run `make golden`,
  and commit the updated snapshots with the code.
- `tarcin --demo` shows the UI with simulated chats. `make loadtest`
  measures latency under load and `make storagebench` the storage
  budgets (see [performance](docs/architecture/performance.md)).
- Store changes must keep events order-independent. `make property`
  delivers random scenarios shuffled, duplicated and in random batches
  and compares the end states (`PROPERTY_SEEDS=2000 make property` for a
  longer run).
- Comments and documentation are written in English.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/):
  `feat(ui): …`, `fix(store): …`, `docs: …`, `perf(msglist): …`,
  `test: …`, `build: …`, `ci: …`, `chore: …`.
- Sign off your commits (`git commit -s`) to certify the
  [Developer Certificate of Origin](https://developercertificate.org/).
  By contributing, you agree that your work is licensed under
  GPL-3.0-or-later.
- One logical change per PR. Rebase on `main`, and keep the history
  clean.
- Update `CHANGELOG.md` for user-facing changes.

## Labels

Issue labels are kept in [`.github/labels.tsv`](.github/labels.tsv);
the maintainer applies them with `scripts/sync-labels.sh`.

## Reporting bugs

Include:

- the tarcin version (`tarcin --version`)
- the terminal emulator and `$TERM`
- the steps to reproduce
- relevant log lines from `~/.local/state/tarcin/tarcin.log`

Logs are redacted, but check them anyway before posting. For performance
problems, attach a profile or trace if you can (see
[performance §5](docs/architecture/performance.md#5-instrumentation)).
