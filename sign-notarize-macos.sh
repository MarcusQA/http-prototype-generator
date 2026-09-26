#!/usr/bin/env bash
set -euo pipefail

# Optional release helper for organisations with an Apple Developer ID.
# Prerequisites:
#   xcrun notarytool store-credentials "http-prototype-generator" \
#     --apple-id "you@example.com" --team-id "TEAMID" --password "app-specific-password"
#
# Usage:
#   DEVELOPER_ID='Developer ID Application: Example Ltd (TEAMID)' ./sign-notarize-macos.sh
# Optional:
#   NOTARY_PROFILE=http-prototype-generator ./sign-notarize-macos.sh
#
# Run build-all.sh on macOS first. When Xcode's `lipo` is available that build
# also creates dist/prototype-macos-universal containing amd64 + arm64 slices.

DEVELOPER_ID=${DEVELOPER_ID:-}
NOTARY_PROFILE=${NOTARY_PROFILE:-http-prototype-generator}

if [[ -z "$DEVELOPER_ID" ]]; then
  echo "Set DEVELOPER_ID to your Developer ID Application certificate name." >&2
  exit 2
fi

bins=(dist/prototype-macos-x64 dist/prototype-macos-arm64)
if [[ -f dist/prototype-macos-universal ]]; then
  bins+=(dist/prototype-macos-universal)
fi

for bin in "${bins[@]}"; do
  [[ -f "$bin" ]] || { echo "Missing $bin; run ./build-all.sh first." >&2; exit 1; }
  codesign --force --timestamp --options runtime --sign "$DEVELOPER_ID" "$bin"
  codesign --verify --strict --verbose=2 "$bin"
done

mkdir -p release
for bin in "${bins[@]}"; do
  name=$(basename "$bin")
  tmp=$(mktemp -d)
  cp "$bin" "$tmp/prototype"
  cp http_values.csv "$tmp/http_values.csv"
  zipfile="release/http-prototype-generator-${name#prototype-}.zip"
  ditto -c -k --keepParent "$tmp" "$zipfile"
  xcrun notarytool submit "$zipfile" --keychain-profile "$NOTARY_PROFILE" --wait
  rm -rf "$tmp"
done

echo "Signed and notarized macOS release ZIPs are in release/."
echo "Bare command-line executables cannot be stapled like an .app/.pkg; Gatekeeper can retrieve the notarization ticket online."
