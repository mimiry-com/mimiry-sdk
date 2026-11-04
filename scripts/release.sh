#!/usr/bin/env bash
# release.sh — build the mimiry CLI's release binaries and publish them as a
# GitHub release for an existing tag.
#
# Usage:
#   scripts/release.sh v0.1.0 --dry-run   build dist/ and verify it; publish nothing
#   scripts/release.sh v0.1.0             the same, then create the GitHub release
#
# What it builds, in dist/:
#   mimiry_<version>_<os>_<arch>.tar.gz   the binary, LICENSE and README.md
#   SHA256SUMS                            one line per archive
#
# Each binary is stamped with the version at link time, so `mimiry --version`
# says it; the build refuses to go on if the binary for this machine does not.
#
# Publishing needs the tag to exist on origin and to be HEAD, and the working
# tree to be clean: a release whose binaries were built from something other
# than its tag is a release nobody can reproduce. The notes are the version's
# section of CHANGELOG.md, which must exist.
#
# Windows is not built: ssh-agent is reached through a Unix socket
# (mimiry/key.go), which Windows does not have, and nobody has run it there.

set -euo pipefail

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
ROOT="$(dirname "$SCRIPT_DIR")"
MODULE="github.com/mimiry-com/mimiry-sdk"
PLATFORMS=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)

die() { echo "release.sh: $*" >&2; exit 1; }

version="${1:-}"
dry_run=false
[[ "${2:-}" == "--dry-run" ]] && dry_run=true
[[ $# -le 2 ]] || die "usage: scripts/release.sh v<X.Y.Z> [--dry-run]"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: scripts/release.sh v<X.Y.Z> [--dry-run] (got '$version')"

cd "$ROOT"

# The notes first: a release nobody can describe is not built.
notes="$(awk -v h="## $version" '$0 == h {on=1; next} on && /^## / {exit} on {print}' CHANGELOG.md)"
[[ -n "${notes//[[:space:]]/}" ]] || die "CHANGELOG.md has no '## $version' section"

if ! $dry_run; then
  [[ -z "$(git status --porcelain)" ]] || die "the working tree is not clean"
  tag_sha="$(git rev-parse -q --verify "refs/tags/$version^{commit}")" || die "no tag $version here"
  [[ "$tag_sha" == "$(git rev-parse HEAD)" ]] || die "HEAD is not $version"
  remote_sha="$(git ls-remote origin "refs/tags/$version^{}" | cut -f1)"
  [[ -n "$remote_sha" ]] || remote_sha="$(git ls-remote origin "refs/tags/$version" | cut -f1)"
  [[ "$remote_sha" == "$tag_sha" ]] || die "origin's $version is '${remote_sha:-absent}', not $tag_sha"
  command -v gh >/dev/null || die "gh is not installed"
fi

rm -rf dist
mkdir -p dist/stage
for platform in "${PLATFORMS[@]}"; do
  os="${platform%/*}"
  arch="${platform#*/}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X $MODULE/mimiry.linkedVersion=$version" \
    -o dist/stage/mimiry ./cmd/mimiry || die "build failed for $platform"
  if [[ "$os" == "$(go env GOHOSTOS)" && "$arch" == "$(go env GOHOSTARCH)" ]]; then
    got="$(dist/stage/mimiry --version)" || die "the $platform binary did not run"
    [[ "$got" == "mimiry $version "* ]] || die "the $platform binary says '$got', not $version"
    echo "verified: $got"
  fi
  cp LICENSE README.md dist/stage/
  archive="mimiry_${version#v}_${os}_${arch}.tar.gz"
  tar -czf "dist/$archive" -C dist/stage mimiry LICENSE README.md || die "could not pack $archive"
  rm -f dist/stage/*
  echo "built: dist/$archive"
done
rmdir dist/stage
(cd dist && sha256sum ./*.tar.gz | sed 's# \./# #' >SHA256SUMS) || die "could not write SHA256SUMS"
[[ "$(wc -l <dist/SHA256SUMS)" -eq "${#PLATFORMS[@]}" ]] || die "SHA256SUMS does not list every archive"

if $dry_run; then
  echo "dry run: dist/ is built and verified; nothing published"
  exit 0
fi

gh release create "$version" dist/*.tar.gz dist/SHA256SUMS --title "$version" --notes "$notes" \
  || die "gh release create failed"
echo "published: $version"
