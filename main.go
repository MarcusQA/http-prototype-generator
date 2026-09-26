package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFS embed.FS

// buildID is replaced by the release build scripts with a UTC build identifier.
// Keeping a default makes ordinary `go run .` development builds work.
var buildID = "dev"

const applicationID = "http-prototype-generator"

type interaction struct {
	ID                  int          `json:"id"`
	Name                string       `json:"name,omitempty"`
	Method              string       `json:"method"`
	Port                int          `json:"port"`
	EffectivePort       int          `json:"effectivePort"`
	Path                string       `json:"path"`
	Query               string       `json:"query"`
	RequestHeaders      []headerPair `json:"requestHeaders"`
	RequestBody         string       `json:"requestBody"`
	RequestBodyDisplay  string       `json:"requestBodyDisplay"`
	StatusCode          int          `json:"statusCode"`
	ResponseHeaders     []headerPair `json:"responseHeaders"`
	ResponseBody        string       `json:"responseBody"`
	ResponseBodyDisplay string       `json:"responseBodyDisplay"`
	URL                 string       `json:"url"`
	Curl                string       `json:"curl"`
}

type headerPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type executeResult struct {
	Status      string              `json:"status"`
	StatusCode  int                 `json:"statusCode"`
	Headers     map[string][]string `json:"headers"`
	Body        string              `json:"body"`
	BodyDisplay string              `json:"bodyDisplay"`
	DurationMs  int64               `json:"durationMs"`
}

type portMapping struct {
	Requested int `json:"requested"`
	Effective int `json:"effective"`
}

type appInfo struct {
	Application     string        `json:"application"`
	BuildID         string        `json:"buildId"`
	CSV             string        `json:"csv"`
	UIURL           string        `json:"uiUrl"`
	RequestedUIPort int           `json:"requestedUiPort"`
	UIPort          int           `json:"uiPort"`
	PortMappings    []portMapping `json:"portMappings"`
}

type instanceMetadata struct {
	Application string `json:"application"`
	BuildID     string `json:"buildId"`
	CSV         string `json:"csv"`
	UIURL       string `json:"uiUrl"`
	Token       string `json:"token"`
	PID         int    `json:"pid"`
	StartedAt   string `json:"startedAt"`
}

type instanceManager struct {
	dir      string
	metaPath string
}

type app struct {
	interactions    []interaction
	byID            map[int]interaction
	portMap         map[int]int
	servers         []*http.Server
	listeners       []net.Listener
	uiServer        *http.Server
	uiListener      net.Listener
	uiURL           string
	uiPort          int
	requestedUIPort int
	csvPath         string
	controlToken    string
	shutdownOnce    sync.Once
	mu              sync.Mutex
}

func main() {
	csvPath := flag.String("csv", "", "path to HTTP values CSV (default: http_values.csv beside the executable)")
	uiPort := flag.Int("ui-port", 9000, "preferred port for the prototype UI; automatically remapped if unavailable")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser automatically")
	replace := flag.Bool("replace", false, "replace an already-running instance for the same CSV")
	exportPostman := flag.String("export-postman", "", "write a Postman v2.1 collection after ports are allocated")
	flag.Parse()

	if flag.NArg() > 0 {
		*csvPath = flag.Arg(0)
	}
	if *uiPort < 0 || *uiPort > 65535 {
		log.Fatalf("invalid --ui-port %d", *uiPort)
	}

	resolvedCSV, err := resolveCSVPath(*csvPath)
	if err != nil {
		log.Fatalf("CSV error: %v", err)
	}
	*csvPath = resolvedCSV

	mgr, err := newInstanceManager(*csvPath)
	if err != nil {
		log.Printf("Warning: single-instance management disabled: %v", err)
	}
	if mgr != nil {
		if existing, live := mgr.liveInstance(); live {
			if existing.BuildID == buildID && !*replace {
				if *exportPostman != "" {
					if err := downloadExistingCollection(existing.UIURL, *exportPostman); err != nil {
						log.Fatalf("Could not export collection from running instance: %v", err)
					}
					log.Printf("Postman/Bruno collection written to %s", *exportPostman)
				}
				log.Printf("This CSV is already running (build %s): %s", existing.BuildID, existing.UIURL)
				if !*noBrowser {
					_ = openBrowser(existing.UIURL)
				}
				return
			}

			log.Printf("Replacing running instance for this CSV (old build %s, new build %s)", existing.BuildID, buildID)
			if err := requestExistingShutdown(existing); err != nil {
				log.Printf("Warning: could not request clean shutdown of the existing instance: %v", err)
			}
			if err := waitForInstanceToStop(existing.UIURL, 5*time.Second); err != nil {
				log.Printf("Warning: existing instance did not confirm shutdown within 5s; continuing with automatic port remapping")
			}
		}
		if err := mgr.acquire(); err != nil {
			log.Printf("Warning: could not acquire instance lock: %v", err)
			mgr = nil
		} else {
			defer mgr.release()
		}
	}

	rows, err := loadCSV(*csvPath)
	if err != nil {
		log.Fatalf("CSV error: %v", err)
	}
	if len(rows) == 0 {
		log.Fatal("CSV contains no request rows")
	}

	token, err := randomToken()
	if err != nil {
		log.Fatalf("Could not create control token: %v", err)
	}
	a := &app{
		interactions:    rows,
		byID:            make(map[int]interaction),
		portMap:         make(map[int]int),
		requestedUIPort: *uiPort,
		csvPath:         *csvPath,
		controlToken:    token,
	}

	if err := a.startMockServers(); err != nil {
		log.Fatalf("Could not start mock server: %v", err)
	}
	defer a.shutdown()
	a.refreshDerivedFields()

	if *exportPostman != "" {
		if err := a.writePostmanCollection(*exportPostman); err != nil {
			log.Fatalf("Could not write Postman/Bruno collection: %v", err)
		}
		log.Printf("Postman/Bruno collection written to %s", *exportPostman)
	}

	uiListener, actualUIPort, err := listenPreferred(*uiPort)
	if err != nil {
		log.Fatalf("Could not start UI server: %v", err)
	}
	a.uiListener = uiListener
	a.uiPort = actualUIPort
	a.uiURL = fmt.Sprintf("http://127.0.0.1:%d/", actualUIPort)
	if *uiPort != 0 && actualUIPort != *uiPort {
		log.Printf("UI port %d is in use; using %d instead", *uiPort, actualUIPort)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/interactions", a.handleInteractions)
	mux.HandleFunc("/api/execute", a.handleExecute)
	mux.HandleFunc("/api/info", a.handleInfo)
	mux.HandleFunc("/api/health", a.handleInfo)
	mux.HandleFunc("/api/shutdown", a.handleShutdown)
	mux.HandleFunc("/api/export/postman", a.handlePostmanExport)
	mux.HandleFunc("/", serveEmbeddedUI)
	a.uiServer = &http.Server{Handler: mux}

	if mgr != nil {
		meta := instanceMetadata{
			Application: applicationID,
			BuildID:     buildID,
			CSV:         *csvPath,
			UIURL:       a.uiURL,
			Token:       token,
			PID:         os.Getpid(),
			StartedAt:   time.Now().UTC().Format(time.RFC3339),
		}
		if err := mgr.write(meta); err != nil {
			log.Printf("Warning: could not write instance metadata: %v", err)
		}
	}

	log.Printf("Loaded %d request(s) from %s", len(rows), *csvPath)
	for _, mapping := range a.sortedPortMappings() {
		if mapping.Requested == mapping.Effective {
			log.Printf("Mock server listening on http://localhost:%d", mapping.Effective)
		} else {
			log.Printf("Mock port %d is in use; mapped to http://localhost:%d", mapping.Requested, mapping.Effective)
		}
	}
	log.Printf("Prototype UI: %s", a.uiURL)
	log.Printf("Build: %s", buildID)
	log.Printf("Press Ctrl+C to stop")

	if !*noBrowser {
		go func() {
			time.Sleep(350 * time.Millisecond)
			if err := openBrowser(a.uiURL); err != nil {
				log.Printf("Could not open browser automatically: %v", err)
			}
		}()
	}

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	go func() {
		<-interrupt
		log.Printf("Shutting down...")
		a.shutdown()
	}()

	if err := a.uiServer.Serve(uiListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("UI server stopped: %v", err)
	}
}

func resolveCSVPath(requested string) (string, error) {
	// Explicit paths keep normal command-line semantics. Relative explicit paths
	// are resolved from the caller's current working directory.
	if strings.TrimSpace(requested) != "" {
		abs, err := filepath.Abs(requested)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", requested, err)
		}
		return abs, nil
	}

	// With no path supplied, use the executable directory rather than the
	// process working directory. Finder, Explorer and desktop launchers often
	// start applications with a different working directory.
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find executable location: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve executable location: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "http_values.csv"), nil
}

func loadCSV(path string) ([]interaction, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = false
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 1 {
		return nil, errors.New("empty CSV")
	}

	index := map[string]int{}
	for i, h := range records[0] {
		index[normalizeHeading(h)] = i
	}

	required := []string{"method", "port", "path", "query", "request headers", "request body", "status code", "response headers", "response body"}
	for _, h := range required {
		if _, ok := index[h]; !ok {
			return nil, fmt.Errorf("missing required heading %q", h)
		}
	}

	get := func(rec []string, heading string) string {
		i, ok := index[heading]
		if !ok || i >= len(rec) {
			return ""
		}
		return rec[i]
	}

	var out []interaction
	for line, rec := range records[1:] {
		if allEmpty(rec) {
			continue
		}

		port, err := strconv.Atoi(strings.TrimSpace(get(rec, "port")))
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("row %d: invalid port", line+2)
		}
		status, err := strconv.Atoi(strings.TrimSpace(get(rec, "status code")))
		if err != nil || status < 100 || status > 599 {
			return nil, fmt.Errorf("row %d: invalid status code", line+2)
		}
		method := strings.ToUpper(strings.TrimSpace(get(rec, "method")))
		if method == "" {
			return nil, fmt.Errorf("row %d: method is required", line+2)
		}
		p := strings.TrimSpace(get(rec, "path"))
		if p == "" {
			p = "/"
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		q := strings.TrimPrefix(strings.TrimSpace(get(rec, "query")), "?")
		canonicalQ, err := canonicalizeQuery(q)
		if err != nil {
			return nil, fmt.Errorf("row %d: invalid query string: %w", line+2, err)
		}
		reqHeaders, err := parseHeaders(get(rec, "request headers"))
		if err != nil {
			return nil, fmt.Errorf("row %d request headers: %w", line+2, err)
		}
		respHeaders, err := parseHeaders(get(rec, "response headers"))
		if err != nil {
			return nil, fmt.Errorf("row %d response headers: %w", line+2, err)
		}
		u := fmt.Sprintf("http://localhost:%d%s", port, p)
		if canonicalQ != "" {
			u += "?" + canonicalQ
		}
		row := interaction{
			ID:              len(out) + 1,
			Name:            strings.TrimSpace(get(rec, "name")),
			Method:          method,
			Port:            port,
			Path:            p,
			Query:           canonicalQ,
			RequestHeaders:  reqHeaders,
			RequestBody:     get(rec, "request body"),
			StatusCode:      status,
			ResponseHeaders: respHeaders,
			ResponseBody:    get(rec, "response body"),
			URL:             u,
		}
		row.RequestBodyDisplay = formatBodyForDisplay(row.RequestBody)
		row.ResponseBodyDisplay = formatBodyForDisplay(row.ResponseBody)
		row.Curl = buildCurl(row)
		out = append(out, row)
	}
	return out, nil
}

func normalizeHeading(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "\ufeff")))
}

func allEmpty(rec []string) bool {
	for _, s := range rec {
		if strings.TrimSpace(s) != "" {
			return false
		}
	}
	return true
}

func parseHeaders(cell string) ([]headerPair, error) {
	if strings.TrimSpace(cell) == "" {
		return nil, nil
	}
	cell = strings.ReplaceAll(cell, "\r\n", "\n")
	cell = strings.ReplaceAll(cell, "\r", "\n")
	var out []headerPair
	for n, line := range strings.Split(cell, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		i := strings.Index(line, ":")
		if i < 1 {
			return nil, fmt.Errorf("line %d must be in 'Header-Name: value' format", n+1)
		}
		name := strings.TrimSpace(line[:i])
		value := strings.TrimSpace(line[i+1:])
		if name == "" {
			return nil, fmt.Errorf("line %d has an empty header name", n+1)
		}
		out = append(out, headerPair{Name: name, Value: value})
	}
	return out, nil
}

func (a *app) startMockServers() error {
	ports := map[int]bool{}
	for _, row := range a.interactions {
		ports[row.Port] = true
	}
	var sorted []int
	for p := range ports {
		sorted = append(sorted, p)
	}
	sort.Ints(sorted)

	// First reserve every requested port that is actually available. Only after
	// that do we allocate ephemeral ports for conflicts. This prevents an
	// ephemeral allocation from accidentally consuming another requested port.
	listeners := make(map[int]net.Listener, len(sorted))
	var conflicted []int
	for _, requested := range sorted {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", requested))
		if err != nil {
			conflicted = append(conflicted, requested)
			continue
		}
		listeners[requested] = ln
		a.portMap[requested] = listenerPort(ln)
	}
	for _, requested := range conflicted {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, open := range listeners {
				_ = open.Close()
			}
			return fmt.Errorf("allocate fallback for requested port %d: %w", requested, err)
		}
		listeners[requested] = ln
		a.portMap[requested] = listenerPort(ln)
	}

	for _, logicalPort := range sorted {
		p := logicalPort
		ln := listeners[p]
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.handleMock(p, w, r)
		})}
		a.listeners = append(a.listeners, ln)
		a.servers = append(a.servers, srv)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("mock server for requested port %d stopped: %v", p, err)
			}
		}()
	}
	return nil
}

func listenPreferred(preferred int) (net.Listener, int, error) {
	if preferred > 0 {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferred))
		if err == nil {
			return ln, listenerPort(ln), nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	return ln, listenerPort(ln), nil
}

func listenerPort(ln net.Listener) int {
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	_, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(portText)
	return port
}

func (a *app) refreshDerivedFields() {
	a.byID = make(map[int]interaction, len(a.interactions))
	for i := range a.interactions {
		row := &a.interactions[i]
		effective := a.portMap[row.Port]
		if effective == 0 {
			effective = row.Port
		}
		row.EffectivePort = effective
		row.URL = fmt.Sprintf("http://localhost:%d%s", effective, row.Path)
		if row.Query != "" {
			row.URL += "?" + row.Query
		}
		row.Curl = buildCurl(*row)
		a.byID[row.ID] = *row
	}
}

func (a *app) sortedPortMappings() []portMapping {
	out := make([]portMapping, 0, len(a.portMap))
	for requested, effective := range a.portMap {
		out = append(out, portMapping{Requested: requested, Effective: effective})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Requested < out[j].Requested })
	return out
}

func (a *app) handleMock(port int, w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	var candidates []interaction
	for _, row := range a.interactions {
		if row.Port == port && row.Method == r.Method && row.Path == r.URL.Path && queriesMatch(row.Query, r.URL.RawQuery) {
			candidates = append(candidates, row)
		}
	}

	for _, row := range candidates {
		if !requestHeadersMatch(row.RequestHeaders, r.Header) {
			continue
		}
		if !requestBodiesMatch(row.RequestBody, body, row.RequestHeaders, r.Header) {
			continue
		}
		for _, h := range row.ResponseHeaders {
			w.Header().Add(h.Name, h.Value)
		}
		w.WriteHeader(row.StatusCode)
		_, _ = io.WriteString(w, row.ResponseBody)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": "No CSV row matched this request",
		"received": map[string]any{
			"method": r.Method,
			"port":   port,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
			"body":   string(body),
		},
	})
}

func canonicalizeQuery(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "", err
	}
	for key := range values {
		sort.Strings(values[key])
	}
	return values.Encode(), nil
}

func queriesMatch(expectedRaw, actualRaw string) bool {
	expected, err := url.ParseQuery(expectedRaw)
	if err != nil {
		return false
	}
	actual, err := url.ParseQuery(actualRaw)
	if err != nil {
		return false
	}
	if len(expected) != len(actual) {
		return false
	}
	for key, expectedValues := range expected {
		actualValues, ok := actual[key]
		if !ok || len(expectedValues) != len(actualValues) {
			return false
		}
		e := append([]string(nil), expectedValues...)
		a := append([]string(nil), actualValues...)
		sort.Strings(e)
		sort.Strings(a)
		for i := range e {
			if e[i] != a[i] {
				return false
			}
		}
	}
	return true
}

func normalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func requestBodiesMatch(expected string, actual []byte, expectedHeaders []headerPair, actualHeaders http.Header) bool {
	// Header arguments are intentionally not used to decide whether semantic
	// JSON matching applies. If both bodies are valid JSON, JSON semantics win.
	// This makes imported requests robust when clients rewrite Content-Type.
	_ = expectedHeaders
	_ = actualHeaders

	expected = normalizeLineEndings(expected)
	actualText := normalizeLineEndings(string(actual))

	// Exact text after newline normalization is the cheapest match and handles
	// ordinary non-JSON text across Windows/macOS/Linux line endings.
	if expected == actualText {
		return true
	}

	// If both bodies are valid JSON, compare parsed values. Insignificant
	// whitespace, object member order, line endings and number spelling such as
	// 1 versus 1.0 do not affect the match. Array order remains significant.
	expectedJSON, expectedErr := decodeJSON(expected)
	actualJSON, actualErr := decodeJSON(actualText)
	if expectedErr == nil && actualErr == nil {
		return jsonValuesEqual(expectedJSON, actualJSON)
	}
	return false
}

func formatBodyForDisplay(body string) string {
	if body == "" {
		return ""
	}
	normalized := normalizeLineEndings(body)
	v, err := decodeJSON(normalized)
	if err != nil {
		return normalized
	}
	formatted, err := formatJSONValue(v)
	if err != nil {
		return normalized
	}
	return formatted
}

func formatJSONValue(v any) (string, error) {
	var b strings.Builder
	if err := writeJSONValue(&b, v, 0); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeJSONValue(b *strings.Builder, v any, depth int) error {
	indent := func(d int) { b.WriteString(strings.Repeat("  ", d)) }

	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		encoded, _ := json.Marshal(x)
		b.Write(encoded)
	case json.Number:
		b.WriteString(canonicalJSONNumber(string(x)))
	case []any:
		if len(x) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteString("[\n")
		for i, item := range x {
			indent(depth + 1)
			if err := writeJSONValue(b, item, depth+1); err != nil {
				return err
			}
			if i < len(x)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		indent(depth)
		b.WriteByte(']')
	case map[string]any:
		if len(x) == 0 {
			b.WriteString("{}")
			return nil
		}
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		b.WriteString("{\n")
		for i, key := range keys {
			indent(depth + 1)
			encodedKey, _ := json.Marshal(key)
			b.Write(encodedKey)
			b.WriteString(": ")
			if err := writeJSONValue(b, x[key], depth+1); err != nil {
				return err
			}
			if i < len(keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		indent(depth)
		b.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value type %T", v)
	}
	return nil
}

func canonicalJSONNumber(s string) string {
	original := s
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}

	mantissa := s
	exponentText := "0"
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa = s[:i]
		exponentText = s[i+1:]
	}
	exponent, ok := new(big.Int).SetString(exponentText, 10)
	if !ok {
		return original
	}

	integerPart := mantissa
	fractionPart := ""
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		integerPart = mantissa[:i]
		fractionPart = mantissa[i+1:]
	}
	digits := integerPart + fractionPart
	decimalPos := big.NewInt(int64(len(integerPart)))
	decimalPos.Add(decimalPos, exponent)

	for len(digits) > 0 && digits[0] == '0' {
		digits = digits[1:]
		decimalPos.Sub(decimalPos, big.NewInt(1))
	}
	if digits == "" {
		return "0"
	}
	for len(digits) > 1 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
	}

	scientificExponent := new(big.Int).Sub(new(big.Int).Set(decimalPos), big.NewInt(1))
	if scientificExponent.IsInt64() {
		e := scientificExponent.Int64()
		if e >= -6 && e <= 20 {
			pos := int(e + 1)
			var plain string
			switch {
			case pos <= 0:
				plain = "0." + strings.Repeat("0", -pos) + digits
			case pos >= len(digits):
				plain = digits + strings.Repeat("0", pos-len(digits))
			default:
				plain = digits[:pos] + "." + digits[pos:]
			}
			return sign + plain
		}
	}

	significand := digits[:1]
	if len(digits) > 1 {
		significand += "." + digits[1:]
	}
	return sign + significand + "e" + scientificExponent.String()
}

func decodeJSON(s string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(s))
	decoder.UseNumber()
	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return v, nil
}

func jsonValuesEqual(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case json.Number:
		bv, ok := b.(json.Number)
		if !ok {
			return false
		}
		ar, aok := new(big.Rat).SetString(string(av))
		br, bok := new(big.Rat).SetString(string(bv))
		return aok && bok && ar.Cmp(br) == 0
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonValuesEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for key, value := range av {
			other, ok := bv[key]
			if !ok || !jsonValuesEqual(value, other) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func requestHeadersMatch(expected []headerPair, actual http.Header) bool {
	if len(expected) == 0 {
		return true
	}
	wanted := map[string][]string{}
	for _, h := range expected {
		key := http.CanonicalHeaderKey(h.Name)
		wanted[key] = append(wanted[key], h.Value)
	}
	for key, values := range wanted {
		actualValues := actual.Values(key)
		for _, wantedValue := range values {
			found := false
			for _, got := range actualValues {
				if got == wantedValue {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func (a *app) handleInteractions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a.interactions)
}

func (a *app) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	row, ok := a.byID[id]
	if !ok {
		http.Error(w, "unknown id", http.StatusNotFound)
		return
	}

	req, err := http.NewRequest(row.Method, row.URL, strings.NewReader(row.RequestBodyDisplay))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, h := range row.RequestHeaders {
		req.Header.Add(h.Name, h.Value)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	result := executeResult{
		Status:      resp.Status,
		StatusCode:  resp.StatusCode,
		Headers:     resp.Header,
		Body:        string(body),
		BodyDisplay: formatBodyForDisplay(string(body)),
		DurationMs:  time.Since(started).Milliseconds(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (a *app) info() appInfo {
	return appInfo{
		Application:     applicationID,
		BuildID:         buildID,
		CSV:             a.csvPath,
		UIURL:           a.uiURL,
		RequestedUIPort: a.requestedUIPort,
		UIPort:          a.uiPort,
		PortMappings:    a.sortedPortMappings(),
	}
}

func (a *app) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(a.info())
}

func (a *app) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Prototype-Token") != a.controlToken {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = io.WriteString(w, `{"status":"shutting down"}`)
	go func() {
		time.Sleep(100 * time.Millisecond)
		a.shutdown()
	}()
}

func (a *app) handlePostmanExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := a.postmanCollectionJSON()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filename := collectionFilename(a.csvPath)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	_, _ = w.Write(data)
}

func collectionFilename(csvPath string) string {
	base := strings.TrimSuffix(filepath.Base(csvPath), filepath.Ext(csvPath))
	if base == "" || base == "." {
		base = "http-prototype"
	}
	return base + ".postman_collection.json"
}

func (a *app) writePostmanCollection(path string) error {
	data, err := a.postmanCollectionJSON()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

func (a *app) postmanCollectionJSON() ([]byte, error) {
	items := make([]any, 0, len(a.interactions))
	for _, row := range a.interactions {
		request := postmanRequest(row)
		responseHeaders := make([]map[string]any, 0, len(row.ResponseHeaders))
		for _, h := range row.ResponseHeaders {
			responseHeaders = append(responseHeaders, map[string]any{"key": h.Name, "value": h.Value})
		}
		statusText := http.StatusText(row.StatusCode)
		if statusText == "" {
			statusText = "Configured response"
		}
		previewLanguage := "text"
		if _, err := decodeJSON(row.ResponseBodyDisplay); err == nil && row.ResponseBodyDisplay != "" {
			previewLanguage = "json"
		}
		response := map[string]any{
			"name":                     fmt.Sprintf("%d %s", row.StatusCode, statusText),
			"originalRequest":          request,
			"status":                   statusText,
			"code":                     row.StatusCode,
			"_postman_previewlanguage": previewLanguage,
			"header":                   responseHeaders,
			"cookie":                   []any{},
			"body":                     row.ResponseBodyDisplay,
		}
		name := row.Name
		if name == "" {
			name = fmt.Sprintf("%s %s", row.Method, row.Path)
		}
		items = append(items, map[string]any{
			"name":     name,
			"request":  request,
			"response": []any{response},
		})
	}

	name := strings.TrimSuffix(filepath.Base(a.csvPath), filepath.Ext(a.csvPath))
	if name == "" || name == "." {
		name = "HTTP Prototype"
	}
	collection := map[string]any{
		"info": map[string]any{
			"name":        name,
			"description": "Generated by HTTP Prototype Generator. URLs use the effective localhost ports allocated for this running instance.",
			"schema":      "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
		},
		"item": items,
	}
	return json.MarshalIndent(collection, "", "  ")
}

func postmanRequest(row interaction) map[string]any {
	headers := make([]map[string]any, 0, len(row.RequestHeaders))
	for _, h := range row.RequestHeaders {
		headers = append(headers, map[string]any{"key": h.Name, "value": h.Value, "type": "text"})
	}

	pathParts := []string{}
	for _, part := range strings.Split(strings.TrimPrefix(row.Path, "/"), "/") {
		if part != "" {
			pathParts = append(pathParts, part)
		}
	}
	queryParts := []map[string]any{}
	if row.Query != "" {
		values, _ := url.ParseQuery(row.Query)
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			vals := append([]string(nil), values[key]...)
			sort.Strings(vals)
			for _, value := range vals {
				queryParts = append(queryParts, map[string]any{"key": key, "value": value})
			}
		}
	}

	request := map[string]any{
		"method": row.Method,
		"header": headers,
		"url": map[string]any{
			"raw":      row.URL,
			"protocol": "http",
			"host":     []string{"localhost"},
			"port":     strconv.Itoa(row.EffectivePort),
			"path":     pathParts,
			"query":    queryParts,
		},
	}
	if row.RequestBodyDisplay != "" {
		body := map[string]any{"mode": "raw", "raw": row.RequestBodyDisplay}
		if _, err := decodeJSON(row.RequestBodyDisplay); err == nil {
			body["options"] = map[string]any{"raw": map[string]any{"language": "json"}}
		}
		request["body"] = body
	}
	return request
}

func serveEmbeddedUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func buildCurl(row interaction) string {
	parts := []string{
		"--request " + shellQuote(row.Method),
		"--url " + shellQuote(row.URL),
	}
	for _, h := range row.RequestHeaders {
		parts = append(parts, "--header "+shellQuote(http.CanonicalHeaderKey(h.Name)+": "+h.Value))
	}
	if row.RequestBodyDisplay != "" {
		parts = append(parts, "--data-raw "+shellQuote(row.RequestBodyDisplay))
	}

	var b strings.Builder
	b.WriteString("curl")
	for _, part := range parts {
		b.WriteString(" \\\n  ")
		b.WriteString(part)
	}
	return b.String()
}

func shellQuote(s string) string {
	// POSIX-safe quoting; also accepted by Postman/Bruno cURL importers.
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func newInstanceManager(csvPath string) (*instanceManager, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(cacheDir) == "" {
		cacheDir = os.TempDir()
	}
	canonical := filepath.Clean(csvPath)
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	sum := sha256.Sum256([]byte(canonical))
	key := hex.EncodeToString(sum[:12])
	parent := filepath.Join(cacheDir, applicationID, "instances")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	dir := filepath.Join(parent, key)
	return &instanceManager{dir: dir, metaPath: filepath.Join(dir, "instance.json")}, nil
}

func (m *instanceManager) acquire() error {
	if err := os.Mkdir(m.dir, 0o700); err == nil {
		return nil
	} else if !os.IsExist(err) {
		return err
	}

	// Existing directory with no responsive instance is stale (for example
	// after a crash or power loss). Remove it and claim it atomically.
	if _, live := m.liveInstance(); live {
		return errors.New("instance is still running")
	}
	if err := os.RemoveAll(m.dir); err != nil {
		return err
	}
	return os.Mkdir(m.dir, 0o700)
}

func (m *instanceManager) release() {
	_ = os.RemoveAll(m.dir)
}

func (m *instanceManager) write(meta instanceMetadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.metaPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.metaPath)
}

func (m *instanceManager) read() (instanceMetadata, error) {
	var meta instanceMetadata
	data, err := os.ReadFile(m.metaPath)
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

func (m *instanceManager) liveInstance() (instanceMetadata, bool) {
	meta, err := m.read()
	if err != nil || meta.Application != applicationID || meta.UIURL == "" {
		return instanceMetadata{}, false
	}
	client := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(strings.TrimRight(meta.UIURL, "/") + "/api/health")
	if err != nil {
		return instanceMetadata{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return instanceMetadata{}, false
	}
	var info appInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return instanceMetadata{}, false
	}
	if info.Application != applicationID || filepath.Clean(info.CSV) != filepath.Clean(meta.CSV) {
		return instanceMetadata{}, false
	}
	return meta, true
}

func requestExistingShutdown(meta instanceMetadata) error {
	if meta.UIURL == "" || meta.Token == "" {
		return errors.New("existing instance has no control endpoint metadata")
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(meta.UIURL, "/")+"/api/shutdown", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Prototype-Token", meta.Token)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("shutdown endpoint returned %s", resp.Status)
	}
	return nil
}

func waitForInstanceToStop(uiURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	healthURL := strings.TrimRight(uiURL, "/") + "/api/health"
	for time.Now().Before(deadline) {
		resp, err := client.Get(healthURL)
		if err != nil {
			return nil
		}
		_ = resp.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("instance is still responding")
}

func downloadExistingCollection(uiURL, outputPath string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(strings.TrimRight(uiURL, "/") + "/api/export/postman")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("export endpoint returned %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(outputPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

func (a *app) shutdown() {
	a.shutdownOnce.Do(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if a.uiServer != nil {
			_ = a.uiServer.Shutdown(ctx)
		}
		for _, srv := range a.servers {
			_ = srv.Shutdown(ctx)
		}
		if a.uiListener != nil {
			_ = a.uiListener.Close()
		}
		for _, ln := range a.listeners {
			_ = ln.Close()
		}
	})
}
