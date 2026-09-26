#!/bin/sh
# Build and sign on this Mac. Apple Silicon is the only supported architecture.
set -eu
cd "$(dirname "$0")/.."
[ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || { echo 'Release builds require an Apple Silicon Mac.' >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo 'Commit release sources before building.' >&2; exit 1; }
commit=$(git rev-parse HEAD)
export GOOS=darwin GOARCH=arm64 CGO_ENABLED=1
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
    *) echo 'Releases require a Developer ID Application identity.' >&2; exit 1 ;;
esac
export DEVCLEANER_SIGN_IDENTITY="$identity"
version=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' packaging/Info.plist)
case "$version" in ''|*[!0-9.]*) echo 'Invalid release version.' >&2; exit 1 ;; esac
out="$(pwd)/dist/v$version/darwin-arm64"
mkdir -p dist "$out"
# Invalidate publication proof before touching any existing assets.
rm -f "$out/NOTARIZED" "$out/SHA256SUMS"
work=$(mktemp -d "$(pwd)/dist/.release.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
export DEVCLEANER_BUILD_DIR="$work"
printf 'Building DevCleaner %s for Apple Silicon locally...\n' "$version"
go build -trimpath -ldflags '-extldflags=-mmacosx-version-min=12.0' -o "$work/devcleaner" ./cmd/devcleaner
[ "$("$work/devcleaner" --standalone version)" = "DevCleaner $version" ] || { echo 'CLI and bundle versions differ.' >&2; exit 1; }
codesign --force --options runtime --timestamp --sign "$identity" "$work/devcleaner"
./scripts/build-app.sh
cp "$work/devcleaner" "$work/DevCleaner.app/Contents/MacOS/devcleaner"
codesign --force --options runtime --timestamp --sign "$identity" "$work/DevCleaner.app"
codesign --verify --strict --verbose=2 "$work/devcleaner"
codesign --verify --deep --strict --verbose=2 "$work/DevCleaner.app"
cp LICENSE README.md "$work/"
COPYFILE_DISABLE=1 tar -czf "$out/devcleaner_${version}_darwin_arm64.tar.gz" -C "$work" devcleaner LICENSE README.md
ditto -c -k --sequesterRsrc --keepParent "$work/DevCleaner.app" "$out/DevCleaner_${version}_darwin_arm64.zip"
# Construct the drag-to-Applications image locally, ready for Apple's checks.
mkdir "$work/image"
ditto "$work/DevCleaner.app" "$work/image/DevCleaner.app"
ln -s /Applications "$work/image/Applications"
cp LICENSE "$work/image/"
hdiutil create -ov -volname DevCleaner -srcfolder "$work/image" -format UDZO "$out/DevCleaner_${version}_arm64.dmg"
codesign --force --timestamp --sign "$identity" "$out/DevCleaner_${version}_arm64.dmg"
[ -z "$(git status --porcelain)" ] || { echo 'Source changed during the build; commit and rebuild.' >&2; exit 1; }
[ "$(git rev-parse HEAD)" = "$commit" ] || { echo 'Commit changed during the build; rebuild.' >&2; exit 1; }
printf '%s\n' "$commit" > "$out/SOURCE_COMMIT"
printf 'Developer ID signed; Apple verification pending. Not ready for publication.\n' > "$out/SIGNING.txt"
printf 'Signed local build: %s\n' "$out"
