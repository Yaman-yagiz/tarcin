# SPDX-License-Identifier: GPL-3.0-or-later
#
# Developer convenience targets. Packagers call `go build` directly (see
# docs/architecture/packaging.md). This Makefile never overrides GOFLAGS,
# CGO_*FLAGS or LDFLAGS from the environment, so distribution build flags
# pass through untouched.

GO       ?= go
PREFIX   ?= /usr/local
DESTDIR  ?=
BUILDDIR ?= build
# sqlite_fts5 builds mattn/go-sqlite3's bundled SQLite with full-text
# search. The Arch package uses libsqlite3 instead (the system SQLite,
# which has FTS5); test-system-sqlite covers that variant.
TAGS     ?= sqlite_fts5
BENCH    ?= .
FUZZTIME ?= 10s
CI_FUZZTIME ?= 5s
SCDOC ?= scdoc
PROPERTY_SEEDS ?= 200
LOAD_SECONDS ?= 60
REPORT   ?=
RELEASE  ?=

PKG       := github.com/Yaman-yagiz/tarcin
BUILDINFO := $(PKG)/internal/buildinfo

# Version metadata. VERSION stays empty without a release tag, so the
# binary falls back to the toolchain's VCS pseudo-version. DATE is never
# the wall clock: SOURCE_DATE_EPOCH when set, otherwise the commit time.
VERSION ?= $(shell git describe --tags --dirty 2>/dev/null | sed 's/^v//')
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
DATE    ?= $(shell if [ -n "$$SOURCE_DATE_EPOCH" ]; then \
	date -u -d "@$$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ; \
	else TZ=UTC git log -1 --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ 2>/dev/null; fi)

# A command-line -ldflags replaces the one in GOFLAGS, so external linking
# must be repeated here to keep full RELRO and BIND_NOW (Arch Go guidelines).
GO_LDFLAGS := -linkmode=external \
	-X $(BUILDINFO).Version=$(VERSION) \
	-X $(BUILDINFO).Commit=$(COMMIT) \
	-X $(BUILDINFO).Date=$(DATE)
BUILD_FLAGS ?= -trimpath

# Developer tools are pinned in a separate module so they never enter the
# main module's dependency graph or the release build.
GOTOOL := $(GO) tool -modfile=tools/go.mod
# actionlint conflicts with golangci-lint's dependencies, so it runs from
# its own module graph instead of tools/go.mod.
ACTIONLINT := github.com/rhysd/actionlint/cmd/actionlint@v1.7.12

.PHONY: all build config-example test test-system-sqlite property cover ci ci-container aur-check repro-check hardening release loadtest storagebench golden lint lint-actions lint-shell check-spdx vuln licenses fmt vet bench fuzz fuzz-short man check-man tidy check-tidy install uninstall clean help

all: build man config-example ## Build what `make install` installs: binary, man pages, example config (needs scdoc)

build: ## Build ./build/tarcin
	$(GO) build $(BUILD_FLAGS) -tags '$(TAGS)' -ldflags '$(GO_LDFLAGS)' -o $(BUILDDIR)/tarcin ./cmd/tarcin

config-example: build ## Write the commented default config to ./build/config.example.toml
	$(BUILDDIR)/tarcin config print-defaults > $(BUILDDIR)/config.example.toml

test: ## Run all tests with the race detector
	$(GO) test -race -tags '$(TAGS)' ./...

test-system-sqlite: ## Run the tests against the system SQLite (-tags libsqlite3)
	$(GO) test -race -tags libsqlite3 ./...

property: ## Run the store's order-independence property test for PROPERTY_SEEDS (200) seeds
	TARCIN_PROPERTY_SEEDS=$(PROPERTY_SEEDS) $(GO) test -count=1 -tags '$(TAGS)' -run Property ./internal/store

ci: check-tidy lint lint-actions lint-shell vuln licenses test test-system-sqlite property fuzz-short check-man build hardening ## Run every CI check locally
	@echo "ci: all checks passed"

ci-container: ## Run `make ci` in the archlinux:base-devel CI image (docker/podman)
	./scripts/ci-container.sh

aur-check: ## Build both AUR packages from the working tree in an Arch container and lint them with namcap
	./scripts/aur-check.sh

repro-check: ## Build HEAD twice from two paths with empty caches and compare the binaries
	GO='$(GO)' ./scripts/repro-check.sh

hardening: ## Build with Arch packaging flags and verify PIE/RELRO/NX
	GO='$(GO)' BUILDDIR='$(BUILDDIR)' ./scripts/check-hardening.sh

release: ## Tag RELEASE=X.Y.Z (signed) and build its artifacts in dist/; publishes nothing
	@[ -n '$(RELEASE)' ] || { echo "usage: make release RELEASE=X.Y.Z"; exit 2; }
	./scripts/release.sh '$(RELEASE)'

cover: ## Write a coverage report to build/coverage.html
	@mkdir -p $(BUILDDIR)
	$(GO) test -tags '$(TAGS)' -coverprofile=$(BUILDDIR)/coverage.out ./...
	$(GO) tool cover -html=$(BUILDDIR)/coverage.out -o $(BUILDDIR)/coverage.html

lint: check-spdx ## Run golangci-lint (formatting included) and the SPDX check
	$(GOTOOL) golangci-lint run --build-tags '$(TAGS)' ./...

lint-actions: ## Lint GitHub Actions workflows (actionlint)
	$(GO) run $(ACTIONLINT) -no-color

lint-shell: ## Lint shell scripts and the bash completion (shellcheck, skipped if not installed)
	@if command -v shellcheck >/dev/null; then shellcheck scripts/*.sh packaging/completions/tarcin.bash && echo "shellcheck: ok"; \
	else echo "shellcheck not installed; skipping"; fi

man: ## Build the man pages into $(BUILDDIR)/man (needs scdoc)
	@mkdir -p $(BUILDDIR)/man
	$(SCDOC) < docs/man/tarcin.1.scd > $(BUILDDIR)/man/tarcin.1.tmp && mv $(BUILDDIR)/man/tarcin.1.tmp $(BUILDDIR)/man/tarcin.1 || { rm -f $(BUILDDIR)/man/tarcin.1.tmp; exit 1; }
	$(SCDOC) < docs/man/tarcin.5.scd > $(BUILDDIR)/man/tarcin.5.tmp && mv $(BUILDDIR)/man/tarcin.5.tmp $(BUILDDIR)/man/tarcin.5 || { rm -f $(BUILDDIR)/man/tarcin.5.tmp; exit 1; }

check-man: ## Build the man pages and validate the desktop file and completions (each skipped if its tool is missing)
	@if command -v $(SCDOC) >/dev/null; then $(MAKE) --no-print-directory man && echo "man: ok"; \
	else echo "scdoc not installed; skipping the man pages"; fi
	@if command -v desktop-file-validate >/dev/null; then desktop-file-validate packaging/tarcin.desktop && echo "desktop file: ok"; \
	else echo "desktop-file-validate not installed; skipping"; fi
	@bash -n packaging/completions/tarcin.bash && echo "bash completion: ok"
	@if command -v zsh >/dev/null; then zsh -n packaging/completions/_tarcin && echo "zsh completion: ok"; \
	else echo "zsh not installed; skipping"; fi
	@if command -v fish >/dev/null; then fish --no-execute packaging/completions/tarcin.fish && echo "fish completion: ok"; \
	else echo "fish not installed; skipping"; fi

check-spdx: ## Check SPDX license headers
	./scripts/check-spdx.sh

vuln: ## Scan for known vulnerabilities (govulncheck)
	$(GOTOOL) govulncheck -tags '$(TAGS)' ./...

licenses: ## Fail on dependencies with GPL-3.0-incompatible licenses
	GO='$(GO)' TAGS='$(TAGS)' ./scripts/check-licenses.sh

vet: ## Run go vet
	$(GO) vet -tags '$(TAGS)' ./...

fmt: ## Format all Go files (gofmt + goimports)
	$(GOTOOL) golangci-lint fmt ./...

loadtest: ## Run the S4–S8 load harness for LOAD_SECONDS (60) and write REPORT
	TARCIN_LOADTEST=1 TARCIN_LOADTEST_SECONDS='$(LOAD_SECONDS)' TARCIN_LOADTEST_REPORT='$(REPORT)' \
		$(GO) test -count=1 -tags '$(TAGS)' -timeout 15m -run 'TestLoad$$' -v ./internal/app

storagebench: ## Measure budgets B1, B2, B6 and B7 on the SQLite store and write REPORT
	TARCIN_STORAGEBENCH=1 TARCIN_STORAGEBENCH_REPORT='$(REPORT)' \
		$(GO) test -count=1 -tags '$(TAGS)' -timeout 30m -run 'TestStorageBenchmarks$$' -v ./internal/app

golden: ## Rewrite the UI golden screen snapshots (review the diff!)
	$(GO) test -count=1 -run TestGolden ./internal/ui -update

bench: ## Run benchmarks (BENCH=regexp)
	$(GO) test -tags '$(TAGS)' -run '^$$' -bench '$(BENCH)' -benchmem ./...

fuzz: ## Run every fuzz target for FUZZTIME each
	GO='$(GO)' TAGS='$(TAGS)' ./scripts/fuzz.sh '$(FUZZTIME)'

fuzz-short: ## Run every fuzz target for CI_FUZZTIME (5s) each, as part of ci
	GO='$(GO)' TAGS='$(TAGS)' ./scripts/fuzz.sh '$(CI_FUZZTIME)'

tidy: ## Tidy go.mod/go.sum of the main and tools modules
	$(GO) mod tidy
	cd tools && $(GO) mod tidy

check-tidy: ## Fail if go.mod or go.sum are not tidy
	$(GO) mod tidy -diff
	cd tools && $(GO) mod tidy -diff

# install only copies what `make` built, so that `sudo make install` never
# builds as root (root-owned files in ./build, root's Go caches).
install: ## Install what `make` built into $(DESTDIR)$(PREFIX); run `make` first, as yourself
	@for f in $(BUILDDIR)/tarcin $(BUILDDIR)/man/tarcin.1 $(BUILDDIR)/man/tarcin.5 $(BUILDDIR)/config.example.toml; do \
		[ -s "$$f" ] || { echo "install: $$f is missing: run make first, as yourself rather than root"; exit 1; }; \
	done
	install -Dm755 $(BUILDDIR)/tarcin $(DESTDIR)$(PREFIX)/bin/tarcin
	install -Dm644 $(BUILDDIR)/man/tarcin.1 $(DESTDIR)$(PREFIX)/share/man/man1/tarcin.1
	install -Dm644 $(BUILDDIR)/man/tarcin.5 $(DESTDIR)$(PREFIX)/share/man/man5/tarcin.5
	install -Dm644 packaging/tarcin.desktop $(DESTDIR)$(PREFIX)/share/applications/tarcin.desktop
	install -Dm644 packaging/completions/tarcin.bash $(DESTDIR)$(PREFIX)/share/bash-completion/completions/tarcin
	install -Dm644 packaging/completions/_tarcin $(DESTDIR)$(PREFIX)/share/zsh/site-functions/_tarcin
	install -Dm644 packaging/completions/tarcin.fish $(DESTDIR)$(PREFIX)/share/fish/vendor_completions.d/tarcin.fish
	install -Dm644 LICENSE $(DESTDIR)$(PREFIX)/share/licenses/tarcin/LICENSE
	install -Dm644 README.md $(DESTDIR)$(PREFIX)/share/doc/tarcin/README.md
	install -Dm644 $(BUILDDIR)/config.example.toml $(DESTDIR)$(PREFIX)/share/doc/tarcin/config.example.toml

uninstall: ## Remove files installed by `make install`
	rm -f $(DESTDIR)$(PREFIX)/bin/tarcin $(DESTDIR)$(PREFIX)/share/man/man1/tarcin.1 $(DESTDIR)$(PREFIX)/share/man/man5/tarcin.5
	rm -f $(DESTDIR)$(PREFIX)/share/applications/tarcin.desktop $(DESTDIR)$(PREFIX)/share/bash-completion/completions/tarcin
	rm -f $(DESTDIR)$(PREFIX)/share/zsh/site-functions/_tarcin $(DESTDIR)$(PREFIX)/share/fish/vendor_completions.d/tarcin.fish
	rm -rf $(DESTDIR)$(PREFIX)/share/licenses/tarcin $(DESTDIR)$(PREFIX)/share/doc/tarcin

clean: ## Remove build output
	rm -rf $(BUILDDIR)

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-13s %s\n", $$1, $$2}'
