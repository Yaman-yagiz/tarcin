#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Fail if a package linked into tarcin carries a license that is not
# compatible with GPL-3.0-or-later (see docs/adr/0007-license-gpl-3.0-or-later.md).
# Usage: scripts/check-licenses.sh   (GO and TAGS from env)
set -eu

go="${GO:-go}"
tags="${TAGS:-}"

# SPDX identifiers as reported by go-licenses (google/licenseclassifier).
allowed="0BSD,Apache-2.0,BSD-2-Clause,BSD-3-Clause,CC0-1.0,GPL-3.0,ISC,LGPL-2.1,LGPL-3.0,MIT,MPL-2.0,Unlicense,Zlib"

# go-licenses logs through klog; keep only warnings and errors.
GOFLAGS="${GOFLAGS:-} -tags=$tags" \
	"$go" tool -modfile=tools/go.mod go-licenses check \
	--allowed_licenses="$allowed" --stderrthreshold=WARNING ./...
echo "check-licenses: ok"
