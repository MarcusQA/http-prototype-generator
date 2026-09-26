#!/usr/bin/env sh
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$DIR"
[ -d dist ] || { echo "dist/ does not exist; run ./build-all.sh first" >&2; exit 1; }
rm -rf release
mkdir -p release/windows release/linux release/macos

cp dist/prototype-windows-x64.exe dist/prototype-windows-arm64.exe http_values.csv start.cmd README.md release/windows/
cp dist/prototype-linux-x64 dist/prototype-linux-arm64 http_values.csv README.md release/linux/
cp start.sh release/linux/
cp dist/prototype-macos-x64 dist/prototype-macos-arm64 http_values.csv README.md start-macos.command start.sh release/macos/
if [ -f dist/prototype-macos-universal ]; then cp dist/prototype-macos-universal release/macos/; fi

# Keep the same dist/ layout expected by the generic launchers inside each bundle.
for os in windows linux macos; do mkdir -p "release/$os/dist"; done
mv release/windows/prototype-windows-*.exe release/windows/dist/
mv release/linux/prototype-linux-* release/linux/dist/
mv release/macos/prototype-macos-* release/macos/dist/
cp http_values.csv release/windows/dist/http_values.csv
cp http_values.csv release/linux/dist/http_values.csv
cp http_values.csv release/macos/dist/http_values.csv

if command -v zip >/dev/null 2>&1; then
  (cd release/windows && zip -qr ../http-prototype-generator-windows.zip .)
  (cd release/macos && zip -qr ../http-prototype-generator-macos.zip .)
fi
if command -v tar >/dev/null 2>&1; then
  tar -C release/linux -czf release/http-prototype-generator-linux.tar.gz .
fi

echo "Release bundles created under release/. Each OS bundle contains both supported architectures and an architecture-detecting launcher."
