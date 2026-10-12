#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Run every fuzz target in the module, one at a time, for a fixed duration.
# Usage: scripts/fuzz.sh [FUZZTIME]   (default 10s; GO and TAGS from env)
set -eu

fuzztime="${1:-10s}"
go="${GO:-go}"
tags="${TAGS:-}"
found=0

for pkg in $("$go" list -tags "$tags" ./...); do
	for target in $("$go" test -tags "$tags" -list '^Fuzz' "$pkg" | grep '^Fuzz' || true); do
		found=$((found + 1))
		echo "==> $pkg $target ($fuzztime)"
		"$go" test -tags "$tags" -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" "$pkg"
	done
done

echo "fuzz: ran $found target(s)"
