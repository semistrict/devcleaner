#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
app_path="${DEVCLEANER_BUILD_DIR:-bin}/DevCleaner.app"
export MACOSX_DEPLOYMENT_TARGET=12.0
export CGO_CFLAGS="${CGO_CFLAGS:--O2 -g} -mmacosx-version-min=12.0"
export CGO_CXXFLAGS="${CGO_CXXFLAGS:--O2 -g} -mmacosx-version-min=12.0"
mkdir -p "$app_path/Contents/MacOS" "$app_path/Contents/Resources"
./scripts/generate-icons.sh
go build -trimpath -tags production -ldflags '-extldflags=-mmacosx-version-min=12.0' -o "$app_path/Contents/MacOS/DevCleaner" ./cmd/devcleaner-app
cp packaging/Info.plist "$app_path/Contents/Info.plist"
cp bin/DevCleaner.icns "$app_path/Contents/Resources/DevCleaner.icns"
# Use a Developer ID identity for a stable distributed identity. The local
# development default is ad-hoc signing; rebuilding may require reapproval.
identity="${DEVCLEANER_SIGN_IDENTITY:--}"
if [ "$identity" = "-" ]; then
    codesign --force --sign - "$app_path"
else
    codesign --force --options runtime --timestamp --sign "$identity" "$app_path"
fi
codesign --verify --strict "$app_path"
printf 'App bundle: %s\n' "$app_path"
