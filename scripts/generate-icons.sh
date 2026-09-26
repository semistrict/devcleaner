#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
iconset="bin/DevCleaner.iconset"
mkdir -p "$iconset" cmd/devcleaner-app/assets
for size in 16 32 128 256 512; do
    sips -z "$size" "$size" packaging/icons/app-icon.png --out "$iconset/icon_${size}x${size}.png" >/dev/null
    double=$((size * 2))
    sips -z "$double" "$double" packaging/icons/app-icon.png --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$iconset" -o bin/DevCleaner.icns
swift scripts/generate-tray-icon.swift cmd/devcleaner-app/assets/tray.png
