#!/bin/sh
# Upload existing local builds; never compile or access signing keys on GitHub.
set -eu
cd "$(dirname "$0")/.."
version=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' packaging/Info.plist)
case "$(uname -m)" in arm64) arch=arm64 ;; x86_64) arch=amd64 ;; *) exit 1 ;; esac
repo=${DEVCLEANER_GITHUB_REPO:-semistrict/devcleaner}
out="dist/v$version/darwin-$arch"
cli="devcleaner_${version}_darwin_${arch}.tar.gz"
app="DevCleaner_${version}_darwin_${arch}.zip"
for asset in "$cli" "$app" SHA256SUMS SIGNING.txt SOURCE_COMMIT; do
    [ -f "$out/$asset" ] || { echo 'Run make release locally first.' >&2; exit 1; }
done
(cd "$out"; shasum -a 256 -c SHA256SUMS)
[ -z "$(git status --porcelain)" ] || { echo 'Commit release sources before uploading.' >&2; exit 1; }
commit=$(git rev-parse HEAD)
[ "$(cat "$out/SOURCE_COMMIT")" = "$commit" ] || { echo 'Assets were built from another commit; run make release again.' >&2; exit 1; }
[ "$(gh api "repos/$repo/commits/main" --jq .sha)" = "$commit" ] || { echo 'Push the release source commit to main before uploading.' >&2; exit 1; }
notes=$(mktemp)
trap 'rm -f "$notes"' EXIT HUP INT TERM
{
    printf 'Local macOS build for %s. Includes the CLI and optional menu-bar app.\n\n' "$arch"
    cat "$out/SIGNING.txt"
    printf '\nVerify downloads using SHA256SUMS. Source commit: %s.\n' "$commit"
} > "$notes"
# Creation intentionally fails for an existing tag; never replace published assets.
gh release create "v$version" --repo "$repo" --target "$commit" --title "DevCleaner $version" --notes-file "$notes" \
    "$out/$cli" "$out/$app" "$out/SHA256SUMS" "$out/SIGNING.txt" "$out/SOURCE_COMMIT"
