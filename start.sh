#!/usr/bin/env sh
set -eu
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
OS=$(uname -s)
ARCH=$(uname -m)

case "$OS:$ARCH" in
  Darwin:arm64) BIN="$DIR/dist/prototype-macos-arm64" ;;
  Darwin:x86_64) BIN="$DIR/dist/prototype-macos-x64" ;;
  Linux:aarch64|Linux:arm64) BIN="$DIR/dist/prototype-linux-arm64" ;;
  Linux:x86_64|Linux:amd64) BIN="$DIR/dist/prototype-linux-x64" ;;
  *) echo "Unsupported platform: $OS $ARCH" >&2; exit 1 ;;
esac

if [ ! -f "$BIN" ]; then
  echo "Could not find $BIN" >&2
  echo "Build the app first, or use a release package containing dist/." >&2
  exit 1
fi

chmod +x "$BIN" 2>/dev/null || true
exec "$BIN" --csv "$DIR/prototype.csv" "$@"
