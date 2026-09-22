package main

import (
	"context"
	"embed"
	"encoding/csv"
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

type interaction struct {
	ID                  int          `json:"id"`
	Name                string       `json:"name,omitempty"`
	Method              string       `json:"method"`
	Port                int          `json:"port"`
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

type app struct {
	interactions []interaction
	byID         map[int]interaction
	servers      []*http.Server
	listeners    []net.Listener
	mu           sync.Mutex
}

func main() {
	csvPath := flag.String("csv", "", "path to prototype CSV (default: prototype.csv beside the executable)")
	uiPort := flag.Int("ui-port", 9000, "port for the prototype UI")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser automatically")
	flag.Parse()

	if flag.NArg() > 0 {
		*csvPath = flag.Arg(0)
	}

	resolvedCSV, err := resolveCSVPath(*csvPath)
	if err != nil {
		log.Fatalf("CSV error: %v", err)
	}
	*csvPath = resolvedCSV

	rows, err := loadCSV(*csvPath)
	if err != nil {
		log.Fatalf("CSV error: %v", err)
	}
	if len(rows) == 0 {
		log.Fatal("CSV contains no request rows")
	}

	a := &app{interactions: rows, byID: make(map[int]interaction)}
	for _, row := range rows {
		a.byID[row.ID] = row
	}

	if err := a.startMockServers(); err != nil {
		log.Fatalf("Could not start mock server: %v", err)
	}
	defer a.shutdown()

	uiAddr := fmt.Sprintf("127.0.0.1:%d", *uiPort)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/interactions", a.handleInteractions)
	mux.HandleFunc("/api/execute", a.handleExecute)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/", serveEmbeddedUI)

	uiURL := fmt.Sprintf("http://%s/", uiAddr)
	log.Printf("Loaded %d request(s) from %s", len(rows), *csvPath)
	log.Printf("Prototype UI: %s", uiURL)
	log.Printf("Press Ctrl+C to stop")

	if !*noBrowser {
		go func() {
			time.Sleep(350 * time.Millisecond)
			if err := openBrowser(uiURL); err != nil {
				log.Printf("Could not open browser automatically: %v", err)
			}
		}()
	}

	if err := http.ListenAndServe(uiAddr, mux); err != nil {
		log.Fatal(err)
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
	return filepath.Join(filepath.Dir(exe), "prototype.csv"), nil
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

	for _, port := range sorted {
		p := port
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			return fmt.Errorf("port %d: %w", p, err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.handleMock(p, w, r)
		})}
		a.listeners = append(a.listeners, ln)
		a.servers = append(a.servers, srv)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("mock server on port %d stopped: %v", p, err)
			}
		}()
		log.Printf("Mock server listening on http://localhost:%d", p)
	}
	return nil
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
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, srv := range a.servers {
		_ = srv.Shutdown(ctx)
	}
	for _, ln := range a.listeners {
		_ = ln.Close()
	}
}
