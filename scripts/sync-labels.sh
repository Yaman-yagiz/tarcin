#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Create or update the repository's issue labels from .github/labels.tsv
# with the GitHub CLI. Labels not in the file are left alone.
# Usage: scripts/sync-labels.sh   (run by the maintainer; needs gh auth)
set -eu

file="$(git rev-parse --show-toplevel)/.github/labels.tsv"
tab="$(printf '\t')"
grep -v '^#' "$file" | while IFS="$tab" read -r name color description; do
	[ -n "$name" ] || continue
	gh label create "$name" --color "$color" --description "$description" --force
done
