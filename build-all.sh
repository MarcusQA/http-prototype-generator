#!/usr/bin/env sh
set -eu
mkdir -p dist
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/prototype-windows-x64.exe .
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/prototype-windows-arm64.exe .
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/prototype-macos-x64 .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/prototype-macos-arm64 .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/prototype-linux-x64 .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/prototype-linux-arm64 .
cp prototype.csv dist/prototype.csv
chmod +x dist/prototype-macos-* dist/prototype-linux-* 2>/dev/null || true
echo "Built binaries in dist/ (prototype.csv copied beside them for direct launch)"
