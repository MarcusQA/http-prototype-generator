#!/usr/bin/env bash
set -euo pipefail

# Optional release helper for organisations with an Apple Developer ID.
# Prerequisites:
#   xcrun notarytool store-credentials "http-prototype-runner" \
#     --apple-id "you@example.com" --team-id "TEAMID" --password "app-specific-password"
#
# Usage:
#   DEVELOPER_ID='Developer ID Application: Example Ltd (TEAMID)' ./sign-notarize-macos.sh
# Optional:
#   NOTARY_PROFILE=http-prototype-runner ./sign-notarize-macos.sh

DEVELOPER_ID=${DEVELOPER_ID:-}
NOTARY_PROFILE=${NOTARY_PROFILE:-http-prototype-runner}

if [[ -z "$DEVELOPER_ID" ]]; then
  echo "Set DEVELOPER_ID to your Developer ID Application certificate name." >&2
  exit 2
fi

for bin in dist/prototype-macos-x64 dist/prototype-macos-arm64; do
  [[ -f "$bin" ]] || { echo "Missing $bin; run ./build-all.sh first." >&2; exit 1; }
  codesign --force --timestamp --options runtime --sign "$DEVELOPER_ID" "$bin"
  codesign --verify --strict --verbose=2 "$bin"
done

mkdir -p release
for arch in x64 arm64; do
  tmp=$(mktemp -d)
  cp "dist/prototype-macos-$arch" "$tmp/prototype"
  cp prototype.csv "$tmp/prototype.csv"
  ditto -c -k --keepParent "$tmp" "release/http-prototype-runner-macos-$arch.zip"
  xcrun notarytool submit "release/http-prototype-runner-macos-$arch.zip" \
    --keychain-profile "$NOTARY_PROFILE" --wait
  rm -rf "$tmp"
done

echo "Signed and notarized release ZIPs are in release/."
echo "Bare command-line executables cannot be stapled like an .app/.pkg; Gatekeeper can retrieve the notarization ticket online."
