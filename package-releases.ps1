$ErrorActionPreference = "Stop"
if (-not (Test-Path dist)) { throw "dist/ does not exist; run build-all.ps1 first" }
Remove-Item release -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path release\windows, release\linux, release\macos | Out-Null

New-Item -ItemType Directory -Force -Path release\windows\dist, release\linux\dist, release\macos\dist | Out-Null
Copy-Item dist\prototype-windows-*.exe release\windows\dist\
Copy-Item dist\prototype-linux-* release\linux\dist\
Copy-Item dist\prototype-macos-* release\macos\dist\
foreach ($os in @("windows","linux","macos")) {
  Copy-Item http_values.csv "release\$os\http_values.csv"
  Copy-Item http_values.csv "release\$os\dist\http_values.csv"
  Copy-Item README.md "release\$os\README.md"
}
Copy-Item start.cmd release\windows\start.cmd
Copy-Item start.sh release\linux\start.sh
Copy-Item start.sh release\macos\start.sh
Copy-Item start-macos.command release\macos\start-macos.command

Compress-Archive -Path release\windows\* -DestinationPath release\http-prototype-generator-windows.zip -Force
Compress-Archive -Path release\macos\* -DestinationPath release\http-prototype-generator-macos.zip -Force
Compress-Archive -Path release\linux\* -DestinationPath release\http-prototype-generator-linux.zip -Force
Write-Host "Release bundles created under release/. Each OS bundle contains both supported architectures and an architecture-detecting launcher."
