#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
#
# Prepares a release (docs/architecture/packaging.md §6): checks that the
# tree, the changelog and the release notes are ready, runs every CI
# check and the reproducibility check, creates the signed annotated tag,
# and writes the source tarball,
# SHA256SUMS and their detached PGP signatures to dist/. It pushes and
# publishes nothing; it prints the commands for that.
#
# Usage: scripts/release.sh X.Y.Z
set -eu

die() {
	echo "release: $*" >&2
	exit 1
}

[ $# -eq 1 ] || {
	echo "usage: scripts/release.sh X.Y.Z" >&2
	exit 2
}
version=$1
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'; then
	echo "release: '$version' is not a version such as 0.1.0 (no leading v)" >&2
	exit 2
fi
tag=v$version
notes=docs/releases/$tag.md
name=tarcin-$version

[ -z "$(git status --porcelain)" ] || die "the working tree is not clean"
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "releases are tagged on main"
git fetch --quiet origin main
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || die "main differs from origin/main"
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
	die "$tag exists already"
fi
grep -Eq "^## \[$version\] - [0-9]{4}-[0-9]{2}-[0-9]{2}\$" CHANGELOG.md ||
	die "CHANGELOG.md has no '## [$version] - YYYY-MM-DD' section"
[ -f "$notes" ] || die "$notes is missing"
git config --get user.signingkey >/dev/null || die "git has no user.signingkey: the tag must be signed"
command -v gpg >/dev/null || die "gpg is needed to sign the tarball"

make ci
./scripts/repro-check.sh

git tag -s "$tag" -m "tarcin $version"
git tag -v "$tag"

mkdir -p dist
rm -f "dist/$name.tar.gz" "dist/$name.tar.gz.sig" dist/SHA256SUMS dist/SHA256SUMS.sig
git archive --format=tar.gz --prefix="$name/" -o "dist/$name.tar.gz" "$tag"
(cd dist && sha256sum "$name.tar.gz" >SHA256SUMS)
gpg --detach-sign "dist/$name.tar.gz"
gpg --detach-sign dist/SHA256SUMS
gpg --verify "dist/$name.tar.gz.sig" "dist/$name.tar.gz"
gpg --verify dist/SHA256SUMS.sig dist/SHA256SUMS

cat <<EOF

$tag is tagged and its artifacts are in dist/. Nothing is published yet.
Check them, then:

  git push origin $tag
  gh release create $tag --draft --title "tarcin $version" --notes-file $notes \\
    dist/$name.tar.gz dist/$name.tar.gz.sig dist/SHA256SUMS dist/SHA256SUMS.sig

The release stays a draft until you publish it on GitHub.
EOF
