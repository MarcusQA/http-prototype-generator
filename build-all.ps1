$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path dist | Out-Null
$env:CGO_ENABLED = "0"
$BuildId = if ($env:BUILD_ID) { $env:BUILD_ID } else { (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ") }
$LdFlags = "-s -w -X main.buildID=$BuildId"
$targets = @(
  @{os="windows"; arch="amd64"; out="prototype-windows-x64.exe"},
  @{os="windows"; arch="arm64"; out="prototype-windows-arm64.exe"},
  @{os="darwin"; arch="amd64"; out="prototype-macos-x64"},
  @{os="darwin"; arch="arm64"; out="prototype-macos-arm64"},
  @{os="linux"; arch="amd64"; out="prototype-linux-x64"},
  @{os="linux"; arch="arm64"; out="prototype-linux-arm64"}
)
foreach ($t in $targets) {
  $env:GOOS = $t.os
  $env:GOARCH = $t.arch
  Write-Host "Building $($t.out) (build $BuildId)"
  go build -trimpath -ldflags $LdFlags -o (Join-Path "dist" $t.out) .
}
Copy-Item http_values.csv (Join-Path "dist" "http_values.csv") -Force
Set-Content -Path (Join-Path "dist" "BUILD_ID") -Value $BuildId -NoNewline
Write-Host "Built binaries in dist/ (build $BuildId)."
Write-Host "Windows/Linux native executables are architecture-specific; the launchers select the correct one."
Write-Host "Run build-all.sh on macOS with Xcode command-line tools to also create prototype-macos-universal."
