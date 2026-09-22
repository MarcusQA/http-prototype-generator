# HTTP Prototype Runner

A zero-runtime-dependency localhost HTTP prototype driven by a CSV file. Each CSV row defines one request and the response the mock server should return.

## What changed in this version

This version is specifically more robust when cURL requests are imported into Bruno/Postman or sent from different operating systems:

- **Semantic JSON request matching.** If both request bodies are valid JSON, they are compared as JSON values rather than text. Whitespace, indentation, object-property order, CRLF/LF differences, and number spelling such as `1` versus `1.0` do not affect matching. Array order remains significant.
- **JSON body matching does not depend on `Content-Type`.** If both bodies parse as valid JSON, semantic JSON comparison is used. Headers explicitly listed in the CSV (including `Content-Type`, if present) are still request-matching conditions.
- **Normalized text line endings.** Non-JSON text treats Windows CRLF (`\r\n`), classic Mac CR (`\r`), and Unix LF (`\n`) as equivalent.
- **Semantic query matching.** Query parameter order and equivalent URL encoding do not affect matching. Repeated parameters are supported and compared as multisets.
- **Canonical query presentation.** The URL shown in the UI and generated cURL uses a stable query-parameter ordering.
- **Standardized JSON presentation.** Valid JSON request bodies, configured response bodies, and actual response bodies are shown using the same two-space indentation, sorted object keys, normalized line endings, and normalized number representation.
- **Standardized cURL presentation.** Every cURL uses the same multi-line layout and, when the request body is JSON, contains the standardized JSON representation rather than whatever whitespace/order happened to be used in the CSV.

The mock server still returns the **configured response body from the CSV unchanged**. The JSON standardization described above is for the UI presentation. The generated request/cURL uses the standardized request JSON, which is semantically equivalent to the CSV JSON.

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

The build process copies `prototype.csv` into `dist/`. If you directly double-click or run a binary in `dist/` with no CSV argument, the application looks for `prototype.csv` **beside that executable**, not in the process working directory.

You can provide a different CSV explicitly:

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
xattr -dr com.apple.quarantine /path/to/http-prototype-generator
```

Then launch again. `sudo` is not required and does not solve Gatekeeper quarantine.

For wider distribution, the preferred solution is to **code-sign and notarize** the macOS binaries with your organisation's Apple Developer ID. An optional helper is included:

```bash
./sign-notarize-macos.sh
```

It expects a valid `Developer ID Application` certificate and an `xcrun notarytool` keychain profile. See the comments at the top of that script.

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

The mock server first narrows rows by:

- port
- HTTP method
- path
- semantic query parameters
- every request header explicitly listed in the CSV row

It then compares the body.

### JSON bodies

When both bodies are valid JSON, comparison is semantic. These all match one another:

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
    "name" : "Alice",
    "age" : 3e1
}
```

Object-property order is ignored. Array order is **not** ignored because array order is part of JSON semantics.

### Other text bodies

For non-JSON text, line endings are normalized before comparison. Other characters remain significant.

### Query strings

Query strings are parsed before comparison. For example:

```text
?a=1&b=2
```

matches:

```text
?b=2&a=1
```

and:

```text
?name=Alice+Smith
```

matches:

```text
?name=Alice%20Smith
```

Repeated parameters are supported. Their ordering is ignored but values and multiplicity must match.

## UI and cURL formatting

Valid JSON is displayed in a stable format:

- two-space indentation
- object keys sorted alphabetically at every level
- LF line endings
- semantically equivalent number spellings normalized where practical (`1.0` -> `1`, `2e0` -> `2`)

The cURL block uses the same standardized request JSON. A typical command looks like:

```bash
curl \
  --request 'POST' \
  --url 'http://localhost:8081/customers?a=1&b=2' \
  --header 'Content-Type: application/json' \
  --data-raw '{
  "age": 30,
  "name": "Alice"
}'
```

This format is suitable for macOS/Linux shells and cURL importers such as Bruno and Postman. On Windows, direct import into Bruno/Postman, PowerShell-compatible environments, Git Bash, or WSL are the most predictable options for multi-line JSON commands.

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

## Automated tests

Run:

```bash
go test ./...
```

Tests cover:

- Bruno/Windows-style CRLF JSON
- semantic JSON equality with reordered object properties
- numeric equivalence such as `30` and `30.0`
- array-order significance
- text line-ending normalization
- semantic query matching and canonical query display
- standardized JSON display
- standardized cURL generation
- an end-to-end mock-handler match using reordered query parameters and differently formatted JSON

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
