#!/bin/sh
# Finish Apple's verification locally, staple offline tickets, then seal assets.
set -eu
cd "$(dirname "$0")/.."
version=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' packaging/Info.plist)
out="$(pwd)/dist/v$version/darwin-arm64"
profile=${DEVCLEANER_NOTARY_PROFILE:-devcleaner}
[ "$(cat "$out/SOURCE_COMMIT")" = "$(git rev-parse HEAD)" ] || { echo 'Rebuild this source commit before notarizing.' >&2; exit 1; }
rm -f "$out/NOTARIZED" "$out/SHA256SUMS"
work=$(mktemp -d "$(pwd)/dist/.notary.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM
app="DevCleaner_${version}_darwin_arm64.zip"
dmg="DevCleaner_${version}_arm64.dmg"
cli="devcleaner_${version}_darwin_arm64.tar.gz"
xcrun notarytool history --keychain-profile "$profile" --output-format json > "$work/history.json" || {
    echo 'Apple verification login is missing or unavailable. Run make setup-notarization, then make release-notarize.' >&2
    exit 1
}
# The image includes the app and the exact signed standalone CLI binary.
xcrun notarytool submit "$out/$dmg" --keychain-profile "$profile" --wait --output-format json > "$work/notary.json"
[ "$(plutil -extract status raw -o - "$work/notary.json")" = Accepted ] || { cat "$work/notary.json" >&2; exit 1; }
xcrun stapler staple "$out/$dmg"
xcrun stapler validate "$out/$dmg"
ditto -x -k "$out/$app" "$work"
xcrun stapler staple "$work/DevCleaner.app"
xcrun stapler validate "$work/DevCleaner.app"
codesign --verify --deep --strict "$work/DevCleaner.app"
spctl --assess --type execute --verbose=2 "$work/DevCleaner.app"
# Verify the shipped standalone CLI has the same notarized code as the helper.
mkdir "$work/cli"
tar -xzf "$out/$cli" -C "$work/cli"
cmp "$work/cli/devcleaner" "$work/DevCleaner.app/Contents/Helpers/devcleaner"
spctl --assess --type execute --verbose=2 "$work/cli/devcleaner"
"$work/cli/devcleaner" --standalone version
spctl --assess --type open --context context:primary-signature --verbose=2 "$out/$dmg"
ditto -c -k --sequesterRsrc --keepParent "$work/DevCleaner.app" "$out/$app"
printf 'Developer ID signed and notarized by Apple. Apple Silicon; macOS 12 or later.\n' > "$out/SIGNING.txt"
cp "$work/notary.json" "$out/NOTARIZED"
(cd "$out"; shasum -a 256 "$cli" "$app" "$dmg" SIGNING.txt SOURCE_COMMIT NOTARIZED > SHA256SUMS)
printf 'Verified downloads ready: %s\n' "$out"
