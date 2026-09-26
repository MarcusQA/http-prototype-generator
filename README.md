# HTTP prototype generator

A zero-runtime-dependency localhost HTTP prototype driven by a CSV file. Each CSV row defines one request and the response the local mock server should return. The app also shows a canonical cURL for every request and can export the whole CSV as a Postman Collection v2.1 file that can be imported into Postman or Bruno.

## Highlights in this version

### Automatic port allocation

The `port` column remains the requested/logical mock port. The app first tries to bind that exact port. If it is already occupied, the OS chooses a free localhost port instead.

For example:

```text
CSV port 8081 -> actual port 8081
CSV port 8082 -> actual port 53147   (8082 was occupied)
```

All rows with the same CSV port share the same effective port. URLs, generated cURLs, the Send button, and exported Postman/Bruno collections use the effective port.

The UI behaves the same way. It prefers port `9000`; if `9000` is occupied it automatically selects a free port and opens that URL. The UI shows a banner whenever a port was remapped.

Mock listeners are reserved before the UI listener, so if the CSV itself requests port 9000 the mock keeps it and the UI moves.

### Relaunching while an instance is already running

Instances are keyed by the absolute CSV path. This means two different CSV files can run at the same time, while launching the same CSV twice is handled deliberately.

- If the same CSV is already running with the **same build**, the second launch opens the existing UI and exits.
- If the same CSV is already running with a **different build**, the new binary requests a clean shutdown of the old build, waits briefly, then starts itself.
- `--replace` forces replacement even when the build IDs are the same. This is useful with local `go run` / `go build` development builds, which default to build ID `dev`.
- If an old process crashed, stale instance metadata is removed automatically on the next launch.

The control endpoint is bound only to localhost and shutdown requires a random per-process token stored in the user's cache directory.

**First upgrade note:** releases from before this single-instance mechanism do not create instance metadata or expose the authenticated shutdown endpoint. Stop such an older release manually once. Subsequent upgrades can replace running instances automatically.

### Postman / Bruno collection export

The UI has a **Download Postman / Bruno collection** button. It returns a Postman Collection v2.1 JSON file containing every request in the CSV and a configured response example for each request.

The exported collection uses the **effective ports for the currently running instance**. If a requested port was remapped, that remapped port is what Postman/Bruno receives.

You can also request an export when launching:

```bash
./prototype-linux-x64 --csv http_values.csv --export-postman prototype.postman_collection.json
```

The app writes the file after mock ports have been allocated and then continues running.

### Robust request matching and canonical presentation

- **Semantic JSON request matching.** JSON whitespace, indentation, object-property order, CRLF/LF differences, and equivalent number spellings such as `1` / `1.0` / `1e0` do not affect matching. JSON array order remains significant.
- **JSON detection does not depend on `Content-Type`.** If both expected and received bodies parse as JSON, semantic JSON matching is used. Headers explicitly listed in the CSV remain matching conditions.
- **Normalized text line endings.** Non-JSON text treats CRLF, CR, and LF as equivalent.
- **Semantic query matching.** Query parameter order and equivalent URL encoding do not affect matching. Repeated parameters are compared as multisets.
- **Canonical JSON display.** Request JSON, configured response JSON, and actual response JSON are shown with two-space indentation, sorted object keys, normalized line endings, and normalized numbers.
- **Canonical cURL.** Generated cURLs use the canonical request JSON and effective URL.

The mock response body itself is still returned exactly as configured in the CSV; response formatting is standardized for display/export only.

## End-user experience

No Go installation is required when distributing prebuilt binaries.

Keep `http_values.csv` in the package root and launch:

- **Windows:** double-click `start.cmd`
- **macOS:** double-click `start-macos.command`, or run `./start.sh`
- **Linux:** run `./start.sh`

The launchers select the native x64/ARM64 executable automatically.

### Can there be only one binary per operating system?

Not as a normal native Go executable on all three operating systems.

Windows PE and Linux ELF executables are architecture-specific, so an `amd64` executable and an `arm64` executable are different native binaries. There is no standard Windows/Linux equivalent of a macOS fat executable. A custom self-extracting wrapper could bundle both, but it would add complexity, make signing/antivirus behaviour worse, and provide little benefit over the tiny launcher.

macOS is different: Mach-O supports a **universal/fat binary** containing both Intel and Apple Silicon slices. When `build-all.sh` runs on macOS with Xcode's `lipo` available, it additionally creates:

```text
dist/prototype-macos-universal
```

`start.sh` prefers that universal binary when present.

For Windows and Linux the recommended distribution is **one archive per OS**, containing both native architectures plus an architecture-detecting launcher. `package-releases.sh` / `package-releases.ps1` create those bundles.

## CSV columns

Required headings:

```text
method
port
path
query
request headers
request body
status code
response headers
response body
```

Optional heading:

```text
name
```

Header cells can contain multiple lines, one header per line:

```text
Content-Type: application/json
Authorization: Bearer abc123
X-Example: one
X-Example: two
```

Excel, LibreOffice and Numbers quote multiline cells correctly when saving CSV files.

## Request matching

The mock server narrows rows using:

1. requested/logical CSV port
2. method
3. path
4. semantic query parameters
5. every request header explicitly specified in the CSV row
6. request body

The actual socket port can differ from the CSV port when a conflict is detected, but it still routes to the rows belonging to that logical CSV port.

### JSON example

All of these bodies match:

```json
{"name":"Alice","age":30}
```

```json
{
  "age": 30.0,
  "name": "Alice"
}
```

```json
{
  "name": "Alice",
  "age": 3e1
}
```

These arrays do **not** match because array order is meaningful:

```json
[1, 2, 3]
```

```json
[3, 2, 1]
```

### Query example

These match:

```text
?a=1&b=2
```

```text
?b=2&a=1
```

and these match:

```text
?name=Alice+Smith
```

```text
?name=Alice%20Smith
```

## Running from source

Requires Go 1.23 or newer:

```bash
go run . --csv http_values.csv
```

Useful options:

```text
--csv FILE              CSV file; defaults to http_values.csv beside the executable
--ui-port PORT          preferred UI port; defaults to 9000 and falls back automatically
--no-browser            do not open the default browser
--replace               replace the running instance for the same CSV
--export-postman FILE   write a Postman v2.1 collection after ports are allocated
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

The scripts inject a UTC build ID so a newly built binary can distinguish itself from a currently running older build. Set `BUILD_ID` explicitly if your CI already has a release/version identifier.

The build output is:

```text
dist/
  prototype-windows-x64.exe
  prototype-windows-arm64.exe
  prototype-linux-x64
  prototype-linux-arm64
  prototype-macos-x64
  prototype-macos-arm64
  prototype-macos-universal   # only when built on macOS with lipo
  http_values.csv
  BUILD_ID
```

Go is required only on the build machine.

## Creating one download per operating system

After building:

```bash
./package-releases.sh
```

or:

```powershell
./package-releases.ps1
```

This creates OS-specific bundles under `release/`. Each Windows/Linux bundle contains the two native architecture binaries plus the launcher that chooses the right one. The macOS bundle also includes the universal binary when it was produced on a Mac.

## macOS Gatekeeper and signing

Unsigned downloads can be quarantined by macOS. For a trusted development copy:

```bash
xattr -dr com.apple.quarantine /path/to/http-prototype-generator
```

`sudo` is not required and does not solve Gatekeeper quarantine.

For wider distribution, sign and notarize with your organisation's Apple Developer ID. The helper script is:

```bash
DEVELOPER_ID='Developer ID Application: Example Ltd (TEAMID)' ./sign-notarize-macos.sh
```

The credentials and certificate are intentionally not bundled.

## Automated tests

Run:

```bash
go test ./...
go vet ./...
```

Tests cover semantic JSON, line-ending normalization, semantic query matching, canonical display/cURL generation, automatic mock-port fallback, preferred UI-port fallback, and Postman collection generation.

## Security / networking notes

- All listeners bind to `127.0.0.1`; they are not exposed on the LAN by default.
- The shutdown endpoint is localhost-only and requires a random token that is not exposed by the UI API.
- Instance metadata is stored under the current user's cache directory and is keyed by CSV path.
- Generated collections contain the currently effective localhost ports and are therefore tied to that running session if any ports were remapped.
