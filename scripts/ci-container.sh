#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Run `make ci` inside the same archlinux:base-devel image that GitHub CI
# uses. The working tree is mounted read-only and copied, so the container
# never writes into it. Go caches persist in named volumes between runs.
# Usage: scripts/ci-container.sh   (RUNTIME=docker|podman, IMAGE=…)
set -eu

runtime="${RUNTIME:-$(command -v podman >/dev/null 2>&1 && echo podman || echo docker)}"
image="${IMAGE:-archlinux:base-devel}"
root="$(git rev-parse --show-toplevel)"

exec "$runtime" run --rm \
	-v "$root:/src:ro" \
	-v tarcin-ci-gomod:/root/go/pkg/mod \
	-v tarcin-ci-gocache:/root/.cache/go-build \
	-e GOTOOLCHAIN=local -e GOFLAGS=-mod=readonly \
	"$image" bash -euo pipefail -c '
		pacman -Syu --noconfirm --needed go git shellcheck sqlite scdoc desktop-file-utils zsh fish >/dev/null
		git config --global --add safe.directory /src
		git config --global --add safe.directory /src/.git
		git clone --quiet /src /work
		# Include uncommitted changes so local runs test the working tree.
		(cd /src && git diff HEAD --binary) | (cd /work && git apply --allow-empty)
		(cd /src && git ls-files --others --exclude-standard -z) |
			(cd /src && xargs -0r cp --parents -t /work)
		cd /work
		go version
		make ci
	'
