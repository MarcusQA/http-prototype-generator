# HTTP prototype generator

A zero-runtime-dependency prototype tool driven by a CSV file. Each CSV row defines one localhost HTTP request and the response the local mock server should return.

## End-user experience

No Go installation is required when you distribute the prebuilt binaries.

### Recommended launch

Keep `prototype.csv` in the package root and launch:

- **Windows:** double-click `start.cmd`
- **macOS:** double-click `start-macos.command`, or run `./start.sh`
- **Linux:** run `./start.sh`

The launchers automatically select the correct x64/ARM64 binary. On macOS, `arm64` selects Apple Silicon and `x86_64` selects Intel.

The app starts the localhost mock ports, starts the UI on `http://127.0.0.1:9000/`, and opens the default browser.

### Direct binary launch

The build/release process also copies `prototype.csv` into `dist/`. If you directly double-click or run a binary in `dist/` with no CSV argument, the application looks for:

```text
prototype.csv
```

**beside that executable**, not in the process working directory. This makes direct Finder/Explorer launches reliable.

You can still provide a different CSV explicitly:

```bash
./prototype-macos-arm64 /path/to/another.csv
```

or:

```bash
./prototype-macos-arm64 --csv /path/to/another.csv
```

## macOS Gatekeeper

Unsigned downloads can be quarantined by macOS. Symptoms can include the process being immediately `Killed`, or Finder refusing to open it.

For a trusted local/test copy, remove quarantine from the extracted package once:

```bash
xattr -dr com.apple.quarantine /path/to/http-prototype-generator-package
```

Then launch again. `sudo` is not required and does not solve Gatekeeper quarantine.

For wider distribution, the preferred solution is to **code-sign and notarize** the macOS binaries with your organisation's Apple Developer ID rather than asking users to remove quarantine. An optional helper is included:

```bash
./sign-notarize-macos.sh
```

It expects a valid `Developer ID Application` certificate and an `xcrun notarytool` keychain profile. See the comments at the top of that script for setup and usage.

## CSV columns

Required headings:

- `method`
- `port`
- `path`
- `query`
- `request headers`
- `request body`
- `status code`
- `response headers`
- `response body`

Optional heading:

- `name` — friendly label shown in the UI

Header cells can contain multiple lines. Put one header on each line:

```text
Content-Type: application/json
Authorization: Bearer abc123
X-Example: one
X-Example: two
```

Excel, LibreOffice and Numbers quote multiline CSV cells correctly when saving.

## Request matching

The mock server matches a row using:

- port
- HTTP method
- path
- raw query string
- every request header listed in the CSV row
- exact request body

Headers not mentioned by the CSV are ignored, so automatic headers such as `User-Agent` do not prevent a match.

If nothing matches, the mock server returns `404` with a diagnostic JSON response.

## cURL

Every request has a generated cURL command in the UI. It can be copied to a terminal and imported by tools such as Postman and Bruno.

The generated cURL uses POSIX-style single-quote escaping, which is suitable for macOS/Linux shells and cURL importers. On Windows, PowerShell, Git Bash, WSL, or direct Postman/Bruno import is recommended for commands containing JSON or special characters.

## Running from source

For developers with Go installed:

```bash
go run . --csv prototype.csv
```

or:

```bash
go build -o prototype .
./prototype --csv prototype.csv
```

Useful flags:

```text
--csv FILE       CSV file; default is prototype.csv beside the executable
--ui-port PORT   browser UI port (default: 9000)
--no-browser     do not launch the default browser
```

A positional CSV filename also works:

```bash
./prototype another-file.csv
```

## Building standalone binaries

From macOS/Linux:

```bash
./build-all.sh
```

From PowerShell:

```powershell
./build-all.ps1
```

Both produce standalone Windows, macOS and Linux x64/ARM64 binaries under `dist/`, and copy `prototype.csv` beside them for direct launch.

Go is needed only on the build machine, not on end-user machines.

## Notes

- Mock listeners bind only to `127.0.0.1`.
- The UI binds only to `127.0.0.1:9000` by default.
- Ports listed in the CSV must be free when the tool starts.
- If the same method/path/query appears more than once, differentiate rows using request headers and/or request body.
- The macOS signing/notarization helper cannot complete without your Apple Developer credentials and certificate; those are intentionally not bundled.
