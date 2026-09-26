#!/bin/sh
# Compile and Developer ID-sign locally. No signing credentials leave this Mac.
set -eu
cd "$(dirname "$0")/.."
[ -z "$(git status --porcelain)" ] || { echo 'Commit release sources before building.' >&2; exit 1; }
commit=$(git rev-parse HEAD)
[ "$(uname -s)" = Darwin ] || { echo 'Release builds require macOS.' >&2; exit 1; }
case "$(uname -m)" in
    arm64) arch=arm64 ;;
    x86_64) arch=amd64 ;;
    *) echo 'Unsupported host architecture.' >&2; exit 1 ;;
esac
# Avoid labelling a cross-compiled or platform-overridden binary as the host build.
export GOOS=darwin GOARCH="$arch" CGO_ENABLED=1
export MACOSX_DEPLOYMENT_TARGET=12.0
export CGO_CFLAGS="${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=12.0"
export CGO_CXXFLAGS="${CGO_CXXFLAGS:--O2 -g} -mmacosx-version-min=12.0"
identity=${DEVCLEANER_SIGN_IDENTITY:-}
if [ -z "$identity" ]; then
    identities=$(security find-identity -v -p codesigning | sed -n 's/.*"\(Developer ID Application:.*\)"/\1/p')
    count=$(printf '%s\n' "$identities" | sed '/^$/d' | wc -l | tr -d ' ')
    [ "$count" = 1 ] || { echo 'Set DEVCLEANER_SIGN_IDENTITY to one Developer ID Application identity.' >&2; exit 1; }
    identity=$identities
fi
case "$identity" in
    'Developer ID Application: '*) ;;
    *) echo 'Releases require a Developer ID Application identity, not ad-hoc signing.' >&2; exit 1 ;;
esac
export DEVCLEANER_SIGN_IDENTITY="$identity"
version=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' packaging/Info.plist)
case "$version" in ''|*[!0-9.]*) echo 'Invalid release version.' >&2; exit 1 ;; esac
out="$(pwd)/dist/v$version/darwin-$arch"
mkdir -p dist "$out"
work=$(mktemp -d "$(pwd)/dist/.release.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
export DEVCLEANER_BUILD_DIR="$work"
printf 'Building DevCleaner %s for darwin/%s locally...\n' "$version" "$arch"
go build -trimpath -ldflags '-extldflags=-mmacosx-version-min=12.0' -o "$work/devcleaner" ./cmd/devcleaner
[ "$("$work/devcleaner" --standalone version)" = "DevCleaner $version" ] || { echo 'CLI and bundle versions differ.' >&2; exit 1; }
codesign --force --options runtime --timestamp --sign "$identity" "$work/devcleaner"
./scripts/build-app.sh
codesign --verify --strict --verbose=2 "$work/devcleaner"
codesign --verify --deep --strict --verbose=2 "$work/DevCleaner.app"
cli="devcleaner_${version}_darwin_${arch}.tar.gz"
app="DevCleaner_${version}_darwin_${arch}.zip"
cp LICENSE README.md "$work/"
COPYFILE_DISABLE=1 tar -czf "$out/$cli" -C "$work" devcleaner LICENSE README.md
ditto -c -k --sequesterRsrc --keepParent "$work/DevCleaner.app" "$out/$app"
if [ -n "${DEVCLEANER_NOTARY_PROFILE:-}" ]; then
    # Keychain profile refers to credentials already stored locally by notarytool.
    # Submit a ZIP for the CLI; notarytool does not accept tar archives.
    ditto -c -k --keepParent "$work/devcleaner" "$work/cli-notary.zip"
    for archive in "$work/cli-notary.zip" "$out/$app"; do
        xcrun notarytool submit "$archive" --keychain-profile "$DEVCLEANER_NOTARY_PROFILE" --wait --output-format json > "$work/notary.json"
        [ "$(plutil -extract status raw -o - "$work/notary.json")" = Accepted ] || { cat "$work/notary.json" >&2; exit 1; }
    done
    xcrun stapler staple "$work/DevCleaner.app"
    xcrun stapler validate "$work/DevCleaner.app"
    ditto -c -k --sequesterRsrc --keepParent "$work/DevCleaner.app" "$out/$app"
    printf 'Developer ID signed; notarized by Apple.\n' > "$out/SIGNING.txt"
else
    printf 'Developer ID signed; not notarized. macOS Gatekeeper may block first launch.\n' > "$out/SIGNING.txt"
fi
[ -z "$(git status --porcelain)" ] || { echo 'Source changed during the build; commit and rebuild.' >&2; exit 1; }
[ "$(git rev-parse HEAD)" = "$commit" ] || { echo 'Commit changed during the build; rebuild.' >&2; exit 1; }
printf '%s\n' "$commit" > "$out/SOURCE_COMMIT"
(cd "$out"; shasum -a 256 "$cli" "$app" SIGNING.txt SOURCE_COMMIT > SHA256SUMS)
printf 'Release assets: %s\n' "$out"
cat "$out/SIGNING.txt"
