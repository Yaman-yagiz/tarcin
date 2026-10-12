#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Fail if a source file lacks the project's SPDX license header within its
# first three lines. Checks tracked and untracked (non-ignored) files.
# Packaging sources under packaging/aur/ are 0BSD and checked separately.
set -eu

header='SPDX-License-Identifier: GPL-3.0-or-later'
missing=0

for f in $(git ls-files --cached --others --exclude-standard \
	'*.go' '*.sh' 'Makefile' '.golangci.yml' '.github/workflows/*.yml' 'packaging/completions/*' \
	':!:packaging/aur/**'); do
	[ -f "$f" ] || continue # deleted but still in the index
	if ! head -n 3 "$f" | grep -qF "$header"; then
		echo "missing SPDX header: $f"
		missing=$((missing + 1))
	fi
done

if [ "$missing" -ne 0 ]; then
	echo "check-spdx: $missing file(s) without '$header'"
	exit 1
fi
echo "check-spdx: ok"
