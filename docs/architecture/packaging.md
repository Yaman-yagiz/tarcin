# Build, packaging and distribution

- **References:** [Arch package guidelines](https://wiki.archlinux.org/title/Arch_package_guidelines),
  [Go package guidelines](https://wiki.archlinux.org/title/Go_package_guidelines),
  [AUR submission guidelines](https://wiki.archlinux.org/title/AUR_submission_guidelines)
- **Decision:** [ADR-0008](../adr/0008-xdg-paths-and-file-permissions.md)

## 1. Build requirements

| Item | Value |
|---|---|
| Go | `go` directive **1.26** (required by Bubble Tea v2 and whatsmeow). No `toolchain` directive above the Arch `extra` version, so building never downloads a toolchain. |
| cgo | **Required**, for mattn/go-sqlite3 |
| Build tags | `libsqlite3` links the system SQLite (Arch package). Without it, mattn's bundled SQLite (with `sqlite_fts5`) is used, as in `go install` and release binaries. |
| Module path | `github.com/Yaman-yagiz/tarcin` |
| Binary | `tarcin`, from `./cmd/tarcin` |

### Version information

`internal/buildinfo` exposes `Version`, `Commit` and `Date`. They are set
with `-X` at link time, falling back to `runtime/debug.ReadBuildInfo()`.
**Every** `-ldflags` value must start with `-linkmode=external`, because
a command-line `-ldflags` replaces the one in `GOFLAGS` and would
otherwise silently drop full RELRO (see the
[Go package guidelines](https://wiki.archlinux.org/title/Go_package_guidelines)). Builds never
embed the wall-clock time. `Date` comes from `SOURCE_DATE_EPOCH`, or from
the commit time.

### Makefile contract

The Makefile is a convenience for developers only. Packagers call
`go build` directly.

- `make build`, `make test`, `make lint`, `make bench`, `make fuzz`,
  `make man`, `make install DESTDIR=… PREFIX=/usr`.
- It **respects** caller-supplied `GOFLAGS`, `CGO_*FLAGS` and `LDFLAGS`,
  and never overwrites them.

## 2. Installed files

| Path | Source |
|---|---|
| `/usr/bin/tarcin` | binary |
| `/usr/share/man/man1/tarcin.1.gz` | `docs/man/tarcin.1.scd` (scdoc) |
| `/usr/share/man/man5/tarcin.5.gz` | config reference (scdoc) |
| `/usr/share/applications/tarcin.desktop` | `Terminal=true`. Lets launchers start it and lets notifications reference `desktop-entry=tarcin` |
| `/usr/share/bash-completion/completions/tarcin` | hand-written completion |
| `/usr/share/zsh/site-functions/_tarcin` | hand-written completion |
| `/usr/share/fish/vendor_completions.d/tarcin.fish` | hand-written completion |
| `/usr/share/licenses/tarcin/LICENSE` | GPL-3.0 text |
| `/usr/share/doc/tarcin/` | README, example `config.toml` |

## 3. Runtime paths (XDG)

| Kind | Path |
|---|---|
| Config | `$XDG_CONFIG_HOME/tarcin/config.toml` (default `~/.config`) |
| Data | `$XDG_DATA_HOME/tarcin/profiles/<name>/` (default `~/.local/share`) |
| State | `$XDG_STATE_HOME/tarcin/` (default `~/.local/state`): logs, compose history, drafts while `$EDITOR` edits them |
| Cache | `$XDG_CACHE_HOME/tarcin/` (default `~/.cache`): media, safe to delete |
| Runtime | `$XDG_RUNTIME_DIR/tarcin/`: reserved for a future control socket |

Resolution follows the XDG spec: empty, unset or relative values fall
back to the defaults. The details and the permission rules are in
[ADR-0008](../adr/0008-xdg-paths-and-file-permissions.md).

## 4. Desktop integration

- **Notifications:** `org.freedesktop.Notifications` over D-Bus via
  `godbus/dbus/v5` (pure Go, no libnotify). Every call has a 2 s timeout.
  `replaces_id` keeps one notification per chat, and the hints are
  `category=im.received` and `desktop-entry=tarcin`. Markup is stripped
  unless the server reports `body-markup`. Without a session bus, tarcin
  falls back to the terminal bell.
- **Clipboard:** OSC 52, which works over SSH. Optional `wl-copy` /
  `xclip` can be configured as an external command.
- **Media opening:** `xdg-open` by default, or per-MIME openers from the
  config.
- **Secret Service:** not used in v1. It may hold the key if app-level
  encryption is added ([ADR-0010](../adr/0010-encryption-at-rest-deferred.md)).

## 5. AUR packages

| Package | Source | Notes |
|---|---|---|
| `tarcin` | Signed release tarball | Built from source with the Arch Go flags. Links the system `sqlite` |
| `tarcin-git` | `git+https://github.com/Yaman-yagiz/tarcin.git` | `provides`/`conflicts` `tarcin` |
| `tarcin-bin` | Release binary (optional, later) | Bundled SQLite. Only if users ask for it |

The PKGBUILDs live in this repository under `packaging/aur/<pkgname>/`
and are pushed to the AUR from there. That directory carries the 0BSD
`LICENSE` and `REUSE.toml` that Arch RFC 40/52 expect for packaging
sources.

### The PKGBUILDs

[`packaging/aur/tarcin/PKGBUILD`](../../packaging/aur/tarcin/PKGBUILD)
builds the signed release tarball and
[`packaging/aur/tarcin-git/PKGBUILD`](../../packaging/aur/tarcin-git/PKGBUILD)
the repository's head, which `provides` and `conflicts` `tarcin`. Both:

- download the modules in `prepare()`, and build with the Arch flags:
  `CGO_*FLAGS` from makepkg, `-buildmode=pie -trimpath -mod=readonly`,
  `-ldflags` starting with `-linkmode=external`, and `-tags libsqlite3`
  for the system SQLite;
- set the version, and the date from `SOURCE_DATE_EPOCH`, with `-X`;
- run the tests in `check()`;
- install the binary, the man pages, the desktop file, the completions,
  the license, the README and an example config generated by
  `tarcin config print-defaults`.

The release package's checksums and `validpgpkeys` are filled in when
the first signed release exists (`updpkgsums`).

`make aur-check` builds both from the working tree in a fresh
`archlinux:base-devel` container, as an unprivileged user: the release
package from a `git archive` in place of the tarball, without its
checksums and signature, and the git package from a local copy. It runs
`namcap` on each PKGBUILD and package, checks the binary for PIE, full
RELRO and NX, checks the installed files, and fails if a `.SRCINFO` is
stale.

### Pre-publish checklist

- [ ] `make aur-check` passes: `namcap PKGBUILD` and
      `namcap tarcin-*.pkg.tar.zst` are clean, apart from documented
      exceptions (for example `elffile-noshstk`).
- [ ] The release PKGBUILD has the release's checksums (`updpkgsums`) and
      the signing key's fingerprint in `validpgpkeys`.
- [ ] The package builds in a clean chroot: `pkgctl build`, or
      `extra-x86_64-build -c`.
- [ ] `checksec --file=/usr/bin/tarcin` shows PIE, full RELRO, NX and a
      canary where applicable.
- [ ] `makepkg --printsrcinfo > .SRCINFO` has been regenerated.
- [ ] Reproducibility: two clean builds produce identical packages
      (checked with `makerepropkg`).
- [ ] The `# Maintainer:` line names the AUR account and an address
      that receives mail, written as `name at domain dot tld`; the
      repository carries a placeholder.

### Pushing to the AUR

`tarcin-git` can go up once the repository is public; `tarcin` once the
first signed release is on GitHub and its PKGBUILD has the checksums.
Each package is its own git repository on the AUR, whose branch must be
`master`:

```sh
git -c init.defaultBranch=master clone ssh://aur@aur.archlinux.org/tarcin-git.git
cd tarcin-git
git config user.name 'NAME'               # the AUR shows both
git config user.email 'ADDRESS@DOMAIN.TLD'
cp ../tarcin/packaging/aur/tarcin-git/{PKGBUILD,LICENSE,REUSE.toml} .
makepkg -od                               # sets pkgver from the clone
makepkg --printsrcinfo > .SRCINFO
git add PKGBUILD .SRCINFO LICENSE REUSE.toml
git commit -m 'Initial upload: 0.1.0'
git push
```

The AUR account needs the SSH public key (My Account → SSH Public Key).
`tarcin` is copied with its `.SRCINFO` as it is, since its version is
fixed. `makepkg -od` leaves `src/` and a clone of the repository next
to the PKGBUILD; add only the four files. Later updates copy the files
again and commit with the new version in the message. The commit's
name and address are public, so they should match the `# Maintainer:`
line rather than a personal identity.

## 6. Releases

- **Versioning:** SemVer, starting at `0.x` until 1.0. `CHANGELOG.md`
  follows Keep a Changelog.
- **Tags:** signed annotated tags `vX.Y.Z`.
- **Artifacts** for each release:
  - `tarcin-X.Y.Z.tar.gz`, a `git archive` of the tag
  - its `.sig`
  - `SHA256SUMS` and `SHA256SUMS.sig`
  - optional x86_64 and aarch64 binaries, built in CI with
    `-trimpath`, bundled SQLite and `SOURCE_DATE_EPOCH`
- **Cadence:** at least one release per month while whatsmeow keeps
  shipping protocol updates, and an immediate release when the server
  reports `ClientOutdated`. A scheduled CI
  job opens a PR that bumps whatsmeow to its latest pseudo-version, so
  the bump gets tested.
- **Go security releases** trigger a `pkgrel` bump in the AUR, because
  Go binaries embed the stdlib.
- **Steps**, for the maintainer, whose PGP key signs the tag and the
  artifacts:
  1. Test the commit to be released on a real account: linking,
     history sync, sending and receiving, media, and logout.
  2. In `CHANGELOG.md`, rename `[Unreleased]` to `[X.Y.Z] - YYYY-MM-DD`
     and start a new, empty `[Unreleased]`. Write the user-facing notes
     in `docs/releases/vX.Y.Z.md`. Commit and push both to `main`.
  3. `make release RELEASE=X.Y.Z` checks that the tree is clean and on
     `origin/main`, runs `make ci` and the reproducibility check
     (`make repro-check`: two builds from different paths with empty
     caches must be identical), creates the signed tag and writes the
     tarball, `SHA256SUMS` and their `.sig` files to `dist/`
     (`scripts/release.sh`). It publishes nothing.
  4. Update the AUR packages: the release PKGBUILD's `pkgver`, its
     checksums (`updpkgsums`) and `validpgpkeys`, and both `.SRCINFO`
     files; `make aur-check` must pass.
  5. Push the tag and create a draft GitHub release with the printed
     commands; publish it after a last look.
