$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path dist | Out-Null
$env:CGO_ENABLED="0"
$targets = @(
  @{os="windows"; arch="amd64"; out="prototype-windows-x64.exe"},
  @{os="windows"; arch="arm64"; out="prototype-windows-arm64.exe"},
  @{os="darwin"; arch="amd64"; out="prototype-macos-x64"},
  @{os="darwin"; arch="arm64"; out="prototype-macos-arm64"},
  @{os="linux"; arch="amd64"; out="prototype-linux-x64"},
  @{os="linux"; arch="arm64"; out="prototype-linux-arm64"}
)
foreach ($t in $targets) {
  $env:GOOS=$t.os; $env:GOARCH=$t.arch
  go build -trimpath -ldflags="-s -w" -o (Join-Path "dist" $t.out) .
}
Copy-Item http_values.csv (Join-Path "dist" "http_values.csv") -Force
Write-Host "Built binaries in dist/ (http_values.csv copied beside them for direct launch)"
