#!/bin/sh
# Starts a release: checks main is clean and current, then tags vX.Y.Z and pushes the tag. CI runs
# the tests, builds every installer and makes a draft release; publishing it (a person's step)
# updates the Homebrew cask.
#
#   scripts/release.sh 0.1.0
set -eu

version=${1:?usage: scripts/release.sh X.Y.Z}
echo "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo "$version is not X.Y.Z" >&2; exit 1; }
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || { echo "not on main" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "the working tree is not clean" >&2; exit 1; }
git fetch -q origin main --tags
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "main is not at origin/main" >&2; exit 1; }
git rev-parse -q --verify "refs/tags/v$version" >/dev/null && { echo "v$version already exists" >&2; exit 1; }
git tag -a "v$version" -m "BenchPod Desktop $version"
git push origin "v$version"
echo "Pushed v$version. Follow the build: gh run list --workflow=release"
