#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Check that the build is reproducible (docs/architecture/security.md §6):
# build HEAD twice, from two copies at different paths, with empty build
# caches and the packaging flags, and compare the binaries byte for byte.
# Usage: scripts/repro-check.sh   (GO and TAGS from env)
set -eu

go="${GO:-go}"
tags="${TAGS:-libsqlite3}"
root="$(git rev-parse --show-toplevel)"
work="$(mktemp -d)"
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT

epoch="$(git -C "$root" log -1 --format=%ct)"
commit="$(git -C "$root" rev-parse --short=12 HEAD)"
date="$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ)"
bi=github.com/Yaman-yagiz/tarcin/internal/buildinfo

for n in 1 2; do
	dir="$work/build-$n/tarcin-src-$n"
	mkdir -p "$dir"
	git -C "$root" archive HEAD | tar -x -C "$dir"
	(
		cd "$dir"
		GOCACHE="$work/cache-$n" GOFLAGS='-buildmode=pie -trimpath -mod=readonly' SOURCE_DATE_EPOCH="$epoch" \
			"$go" build -tags "$tags" -o "$work/tarcin-$n" \
			-ldflags "-linkmode=external -X $bi.Commit=$commit -X $bi.Date=$date" ./cmd/tarcin
	)
done

a="$(sha256sum "$work/tarcin-1" | cut -d' ' -f1)"
b="$(sha256sum "$work/tarcin-2" | cut -d' ' -f1)"
if [ "$a" != "$b" ]; then
	echo "repro-check: the two builds differ: $a and $b"
	exit 1
fi
echo "repro-check: two builds are identical ($a)"
