#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Build tarcin the way the Arch package does (docs/architecture/packaging.md)
# and verify the binary is a PIE with full RELRO and a non-executable stack.
# Uses the compiler flags from /etc/makepkg.conf when it exists.
set -euo pipefail

go="${GO:-go}"
out="${BUILDDIR:-build}/tarcin-hardened"
tags="${HARDENING_TAGS:-libsqlite3}" # as the Arch package builds it

if [[ -r /etc/makepkg.conf ]]; then
	# shellcheck disable=SC1091 # system file, not part of this repository
	source /etc/makepkg.conf
fi
export CGO_CPPFLAGS="${CPPFLAGS:-}" CGO_CFLAGS="${CFLAGS:-}"
export CGO_CXXFLAGS="${CXXFLAGS:-}" CGO_LDFLAGS="${LDFLAGS:-}"
export GOFLAGS="-buildmode=pie -trimpath -ldflags=-linkmode=external -mod=readonly -modcacherw"

"$go" build -tags "$tags" -o "$out" \
	-ldflags "-linkmode=external -X github.com/Yaman-yagiz/tarcin/internal/buildinfo.Version=0.0.0-hardening" \
	./cmd/tarcin
"$out" --version

fail() {
	echo "check-hardening: $1" >&2
	exit 1
}
readelf -hW "$out" | grep -q 'DYN (Position-Independent Executable file)' || fail "not a PIE"
readelf -lW "$out" | grep -q GNU_RELRO || fail "no GNU_RELRO segment"
readelf -dW "$out" | grep -q BIND_NOW || fail "no BIND_NOW (partial RELRO)"
if readelf -lW "$out" | grep GNU_STACK | grep -q RWE; then
	fail "executable stack"
fi
echo "check-hardening: PIE, full RELRO and non-executable stack verified"
