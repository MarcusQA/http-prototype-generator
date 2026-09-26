@echo off
setlocal
cd /d "%~dp0"
set "BIN=dist\prototype-windows-x64.exe"
if /I "%PROCESSOR_ARCHITECTURE%"=="ARM64" set "BIN=dist\prototype-windows-arm64.exe"
if /I "%PROCESSOR_ARCHITEW6432%"=="ARM64" set "BIN=dist\prototype-windows-arm64.exe"
if not exist "%BIN%" (
  echo Could not find %BIN%
  echo Build the app first with build-all.ps1, or use a release package containing dist\.
  exit /b 1
)
"%BIN%" --csv "%~dp0http_values.csv" %*
endlocal
