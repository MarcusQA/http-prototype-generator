package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
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
	ID              int          `json:"id"`
	Name            string       `json:"name,omitempty"`
	Method          string       `json:"method"`
	Port            int          `json:"port"`
	Path            string       `json:"path"`
	Query           string       `json:"query"`
	RequestHeaders  []headerPair `json:"requestHeaders"`
	RequestBody     string       `json:"requestBody"`
	StatusCode      int          `json:"statusCode"`
	ResponseHeaders []headerPair `json:"responseHeaders"`
	ResponseBody    string       `json:"responseBody"`
	URL             string       `json:"url"`
	Curl            string       `json:"curl"`
}

type headerPair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type executeResult struct {
	Status     string              `json:"status"`
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
	DurationMs int64               `json:"durationMs"`
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
		reqHeaders, err := parseHeaders(get(rec, "request headers"))
		if err != nil {
			return nil, fmt.Errorf("row %d request headers: %w", line+2, err)
		}
		respHeaders, err := parseHeaders(get(rec, "response headers"))
		if err != nil {
			return nil, fmt.Errorf("row %d response headers: %w", line+2, err)
		}
		u := fmt.Sprintf("http://localhost:%d%s", port, p)
		if q != "" {
			u += "?" + q
		}
		row := interaction{
			ID:              len(out) + 1,
			Name:            strings.TrimSpace(get(rec, "name")),
			Method:          method,
			Port:            port,
			Path:            p,
			Query:           q,
			RequestHeaders:  reqHeaders,
			RequestBody:     get(rec, "request body"),
			StatusCode:      status,
			ResponseHeaders: respHeaders,
			ResponseBody:    get(rec, "response body"),
			URL:             u,
		}
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
		if row.Port == port && row.Method == r.Method && row.Path == r.URL.Path && row.Query == r.URL.RawQuery {
			candidates = append(candidates, row)
		}
	}

	for _, row := range candidates {
		if !requestHeadersMatch(row.RequestHeaders, r.Header) {
			continue
		}
		if row.RequestBody != string(body) {
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

	req, err := http.NewRequest(row.Method, row.URL, strings.NewReader(row.RequestBody))
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
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       string(body),
		DurationMs: time.Since(started).Milliseconds(),
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
	var b strings.Builder
	b.WriteString("curl --request ")
	b.WriteString(shellQuote(row.Method))
	b.WriteString(" --url ")
	b.WriteString(shellQuote(row.URL))
	for _, h := range row.RequestHeaders {
		b.WriteString(" \\\n  --header ")
		b.WriteString(shellQuote(h.Name + ": " + h.Value))
	}
	if row.RequestBody != "" {
		b.WriteString(" \\\n  --data-raw ")
		b.WriteString(shellQuote(row.RequestBody))
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

// Retain imports used by some Go versions' optimizer/build paths.
var _ = bytes.MinRead
var _ = url.QueryEscape
