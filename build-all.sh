#!/usr/bin/env sh
set -eu

mkdir -p dist
BUILD_ID=${BUILD_ID:-$(date -u +%Y%m%dT%H%M%SZ)}
LDFLAGS="-s -w -X main.buildID=$BUILD_ID"

build() {
  os=$1
  arch=$2
  out=$3
  echo "Building $out (build $BUILD_ID)"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="$LDFLAGS" -o "dist/$out" .
}

build windows amd64 prototype-windows-x64.exe
build windows arm64 prototype-windows-arm64.exe
build darwin amd64 prototype-macos-x64
build darwin arm64 prototype-macos-arm64
build linux amd64 prototype-linux-x64
build linux arm64 prototype-linux-arm64

# A real universal/fat macOS executable can contain both amd64 and arm64 slices.
# lipo is supplied by Xcode, so this step is available only when building on a
# Mac with the Apple command-line tools installed.
if [ "$(uname -s 2>/dev/null || true)" = "Darwin" ] && command -v lipo >/dev/null 2>&1; then
  echo "Creating universal macOS binary"
  lipo -create dist/prototype-macos-x64 dist/prototype-macos-arm64 -output dist/prototype-macos-universal
  chmod +x dist/prototype-macos-universal
fi

cp http_values.csv dist/http_values.csv
chmod +x dist/prototype-macos-* dist/prototype-linux-* 2>/dev/null || true
printf '%s\n' "$BUILD_ID" > dist/BUILD_ID

echo "Built binaries in dist/ (build $BUILD_ID)."
echo "Windows/Linux remain architecture-specific native executables; start.cmd/start.sh select the correct one."
