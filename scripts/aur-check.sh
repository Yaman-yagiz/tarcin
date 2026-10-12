#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Build both AUR packages from this working tree in a fresh
# archlinux:base-devel container, as an unprivileged user, and check them:
# namcap on each PKGBUILD and package, the binary's hardening (PIE, full
# RELRO, NX), the files it installs, and that .SRCINFO is current.
#
# The working tree, uncommitted changes included, is committed in a copy.
# tarcin is built from a `git archive` of it in place of the signed
# release tarball, without the checksums and signature only a tagged
# release has; tarcin-git is built from the copy instead of GitHub.
# A clean chroot (pkgctl build) remains the check before publishing.
# Usage: scripts/aur-check.sh   (RUNTIME=docker|podman, IMAGE=…)
set -eu

runtime="${RUNTIME:-$(command -v podman >/dev/null 2>&1 && echo podman || echo docker)}"
image="${IMAGE:-archlinux:base-devel}"
root="$(git rev-parse --show-toplevel)"

# shellcheck disable=SC2016 # the script is expanded inside the container
exec "$runtime" run --rm \
	-v "$root:/src:ro" \
	-v tarcin-aur-gomod:/gomod \
	-e GOMODCACHE=/gomod -e GOTOOLCHAIN=local \
	"$image" bash -euo pipefail -c '
		pacman -Syu --noconfirm --needed go git scdoc sqlite namcap >/dev/null
		useradd -m builder
		chown builder /gomod
		git config --system --add safe.directory "*"
		su builder -c "bash -euo pipefail" <<"BUILD"
			cd ~
			git clone --quiet /src tarcin-src
			(cd /src && git diff HEAD --binary) | (cd tarcin-src && git apply --allow-empty)
			(cd /src && git ls-files --others --exclude-standard -z) | (cd /src && xargs -0r cp --parents -t ~/tarcin-src)
			(cd tarcin-src && git add -A && git -c user.name=aur-check -c user.email=aur-check@localhost commit -qm "working tree" --allow-empty)
			aur=tarcin-src/packaging/aur
			for d in tarcin tarcin-git; do
				(cd "$aur/$d" && makepkg --printsrcinfo | diff -u .SRCINFO - ) ||
					{ echo "aur-check: $d/.SRCINFO is stale: run makepkg --printsrcinfo > .SRCINFO"; exit 1; }
			done

			ver=$(sed -n "s/^pkgver=//p" "$aur/tarcin/PKGBUILD")
			mkdir rel git
			cp "$aur/tarcin/PKGBUILD" rel/
			(cd tarcin-src && git archive --format=tar.gz --prefix="tarcin-$ver/" -o ~/rel/"tarcin-$ver.tar.gz" HEAD)
			sed -i "s|^source=.*|source=(\"tarcin-$ver.tar.gz\")|; s|^b2sums=.*|b2sums=(SKIP)|" rel/PKGBUILD
			cp "$aur/tarcin-git/PKGBUILD" git/
			sed -i "s|^source=.*|source=(\"tarcin::git+file://$HOME/tarcin-src\")|" git/PKGBUILD

			for d in rel git; do
				echo "==> $d"
				cd ~/"$d"
				namcap PKGBUILD
				makepkg --noconfirm --cleanbuild
				pkg=$(ls ./*.pkg.tar.zst | grep -v -- -debug-) # makepkg also makes a debug package
				namcap "$pkg"
				bin=$(mktemp)
				bsdtar -xOf "$pkg" usr/bin/tarcin >"$bin"
				readelf -hW "$bin" | grep -q "DYN (Position-Independent Executable file)" || { echo "not a PIE"; exit 1; }
				readelf -lW "$bin" | grep -q GNU_RELRO || { echo "no RELRO"; exit 1; }
				readelf -dW "$bin" | grep -q BIND_NOW || { echo "partial RELRO"; exit 1; }
				if readelf -lW "$bin" | grep GNU_STACK | grep -q RWE; then echo "executable stack"; exit 1; fi
				name=$(sed -n "s/^pkgname=//p" PKGBUILD)
				for f in usr/bin/tarcin usr/share/man/man1/tarcin.1.gz usr/share/man/man5/tarcin.5.gz \
					usr/share/applications/tarcin.desktop usr/share/bash-completion/completions/tarcin \
					usr/share/zsh/site-functions/_tarcin usr/share/fish/vendor_completions.d/tarcin.fish \
					"usr/share/licenses/$name/LICENSE" "usr/share/doc/$name/README.md" \
					"usr/share/doc/$name/config.example.toml"; do
					bsdtar -tf "$pkg" | grep -qx "$f" || { echo "aur-check: $name lacks $f"; exit 1; }
				done
				chmod +x "$bin"
				"$bin" --version
			done
			echo "aur-check: both packages built and passed namcap"
BUILD
	'
