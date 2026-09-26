package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestBodiesMatchNormalizesCRLF(t *testing.T) {
	expected := "line one\nline two\n"
	actual := []byte("line one\r\nline two\r\n")
	if !requestBodiesMatch(expected, actual, nil, http.Header{}) {
		t.Fatal("expected LF and CRLF text bodies to match")
	}
}

func TestRequestBodiesMatchJSONSemanticallyWithoutContentType(t *testing.T) {
	expected := `{
  "name": "Alice",
  "age": 30,
  "roles": ["admin", "user"],
  "enabled": true
}`
	actual := []byte("{\r\n  \"enabled\": true,\r\n  \"roles\": [\"admin\",\"user\"],\r\n  \"age\": 30.0,\r\n  \"name\": \"Alice\"\r\n}")
	if !requestBodiesMatch(expected, actual, nil, http.Header{}) {
		t.Fatal("expected semantically equivalent JSON bodies to match without relying on Content-Type")
	}
}

func TestRequestBodiesMatchJSONArraysRemainOrdered(t *testing.T) {
	expected := `[1, 2, 3]`
	actual := []byte(`[3, 2, 1]`)
	if requestBodiesMatch(expected, actual, nil, http.Header{}) {
		t.Fatal("JSON array order must remain significant")
	}
}

func TestFormatBodyForDisplayCanonicalizesJSON(t *testing.T) {
	input := "{\r\n  \"z\": 1.0,\r\n  \"a\": {\"y\":2e0,\"x\":true},\r\n  \"items\": [3,2,1]\r\n}"
	want := `{
  "a": {
    "x": true,
    "y": 2
  },
  "items": [
    3,
    2,
    1
  ],
  "z": 1
}`
	if got := formatBodyForDisplay(input); got != want {
		t.Fatalf("formatted JSON mismatch\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

func TestCanonicalJSONNumber(t *testing.T) {
	cases := map[string]string{
		"1":        "1",
		"1.0":      "1",
		"1e2":      "100",
		"100.000":  "100",
		"0.00100":  "0.001",
		"1e-7":     "1e-7",
		"1e21":     "1e21",
		"-0.0":     "0",
		"-12.3400": "-12.34",
	}
	for input, want := range cases {
		if got := canonicalJSONNumber(input); got != want {
			t.Errorf("canonicalJSONNumber(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestQueriesMatchSemantically(t *testing.T) {
	cases := []struct {
		expected string
		actual   string
		match    bool
	}{
		{"a=1&b=2", "b=2&a=1", true},
		{"name=Alice+Smith", "name=Alice%20Smith", true},
		{"id=1&id=2", "id=2&id=1", true},
		{"flag", "flag=", true},
		{"a=1&b=2", "a=1&b=3", false},
		{"id=1&id=2", "id=1", false},
	}

	for _, tc := range cases {
		if got := queriesMatch(tc.expected, tc.actual); got != tc.match {
			t.Errorf("queriesMatch(%q, %q) = %v, want %v", tc.expected, tc.actual, got, tc.match)
		}
	}
}

func TestCanonicalizeQuery(t *testing.T) {
	got, err := canonicalizeQuery("z=last&id=2&a=hello%20world&id=1")
	if err != nil {
		t.Fatal(err)
	}
	want := "a=hello+world&id=1&id=2&z=last"
	if got != want {
		t.Fatalf("canonicalizeQuery = %q, want %q", got, want)
	}
}

func TestBuildCurlUsesStandardizedJSON(t *testing.T) {
	row := interaction{
		Method:             "POST",
		URL:                "http://localhost:8081/customers?a=1&b=2",
		RequestHeaders:     []headerPair{{Name: "content-type", Value: "application/json"}},
		RequestBodyDisplay: formatBodyForDisplay(`{"b":2.0,"a":1}`),
	}
	got := buildCurl(row)
	want := "curl \\\n  --request 'POST' \\\n  --url 'http://localhost:8081/customers?a=1&b=2' \\\n  --header 'Content-Type: application/json' \\\n  --data-raw '{\n  \"a\": 1,\n  \"b\": 2\n}'"
	if got != want {
		t.Fatalf("cURL mismatch\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

func TestMockMatchesBrunoStyleJSONAndQuery(t *testing.T) {
	row := interaction{
		ID:             1,
		Method:         http.MethodPost,
		Port:           8081,
		Path:           "/customers",
		Query:          "a=1&b=2",
		RequestHeaders: []headerPair{{Name: "Content-Type", Value: "application/json"}},
		RequestBody: `{
  "name": "Alice",
  "age": 30
}`,
		StatusCode:      http.StatusCreated,
		ResponseHeaders: []headerPair{{Name: "Content-Type", Value: "application/json"}},
		ResponseBody:    `{"ok":true}`,
	}
	a := &app{interactions: []interaction{row}}

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8081/customers?b=2&a=1", strings.NewReader("{\r\n  \"age\": 30.0,\r\n  \"name\": \"Alice\"\r\n}"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	a.handleMock(8081, rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusCreated, rr.Body.String())
	}
	if rr.Body.String() != `{"ok":true}` {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestStartMockServersRemapsOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	requested := listenerPort(occupied)

	a := &app{
		interactions: []interaction{{
			ID: 1, Method: http.MethodGet, Port: requested, Path: "/hello",
			StatusCode: http.StatusOK, ResponseBody: `{"ok":true}`,
		}},
		byID:    make(map[int]interaction),
		portMap: make(map[int]int),
	}
	if err := a.startMockServers(); err != nil {
		t.Fatal(err)
	}
	defer a.shutdown()
	a.refreshDerivedFields()
	if a.portMap[requested] == requested {
		t.Fatalf("expected occupied port %d to be remapped", requested)
	}
	resp, err := http.Get(a.interactions[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}
}

func TestListenPreferredUsesFallback(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	preferred := listenerPort(occupied)
	ln, actual, err := listenPreferred(preferred)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if actual == preferred {
		t.Fatalf("expected fallback from occupied port %d", preferred)
	}
}

func TestPostmanCollectionUsesEffectivePortAndExamples(t *testing.T) {
	a := &app{
		csvPath: "/tmp/demo.csv",
		interactions: []interaction{{
			ID: 1, Name: "Create customer", Method: http.MethodPost,
			Port: 8081, EffectivePort: 54321, Path: "/customers", Query: "a=1&b=2",
			URL:                 "http://localhost:54321/customers?a=1&b=2",
			RequestHeaders:      []headerPair{{Name: "Content-Type", Value: "application/json"}},
			RequestBodyDisplay:  "{\n  \"name\": \"Alice\"\n}",
			StatusCode:          201,
			ResponseHeaders:     []headerPair{{Name: "Content-Type", Value: "application/json"}},
			ResponseBodyDisplay: "{\n  \"id\": 1\n}",
		}},
	}
	data, err := a.postmanCollectionJSON()
	if err != nil {
		t.Fatal(err)
	}
	var collection map[string]any
	if err := json.Unmarshal(data, &collection); err != nil {
		t.Fatal(err)
	}
	items := collection["item"].([]any)
	item := items[0].(map[string]any)
	req := item["request"].(map[string]any)
	u := req["url"].(map[string]any)
	if u["port"] != "54321" {
		t.Fatalf("port = %#v", u["port"])
	}
	responses := item["response"].([]any)
	if len(responses) != 1 {
		t.Fatalf("response examples = %d", len(responses))
	}
}

func TestCollectionFilename(t *testing.T) {
	if got := collectionFilename(`/tmp/http_values.csv`); got != "http_values.postman_collection.json" {
		t.Fatalf("got %q", got)
	}
}

func TestReconcileMockServersReloadsRowsWithoutRestart(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	requested := listenerPort(probe)
	_ = probe.Close()

	a := &app{
		interactions: []interaction{{
			ID: 1, Method: http.MethodGet, Port: requested, Path: "/live",
			StatusCode: http.StatusOK, ResponseBody: `{"version":1}`,
		}},
		byID:         make(map[int]interaction),
		portMap:      make(map[int]int),
		mockServers:  make(map[int]*mockServer),
		eventClients: make(map[chan reloadEvent]struct{}),
		done:         make(chan struct{}),
		csvRevision:  1,
	}
	if err := a.startMockServers(); err != nil {
		t.Fatal(err)
	}
	defer a.shutdown()
	a.refreshDerivedFields()

	a.stateMu.RLock()
	url := a.interactions[0].URL
	a.stateMu.RUnlock()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"version":1}` {
		t.Fatalf("initial body = %q", body)
	}

	updated := []interaction{{
		ID: 1, Method: http.MethodGet, Port: requested, Path: "/live",
		StatusCode: http.StatusOK, ResponseBody: `{"version":2}`,
		ResponseBodyDisplay: `{
  "version": 2
}`,
	}}
	if err := a.reconcileMockServers(updated); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != `{"version":2}` {
		t.Fatalf("reloaded body = %q", body)
	}

	a.stateMu.RLock()
	revision := a.csvRevision
	a.stateMu.RUnlock()
	if revision != 2 {
		t.Fatalf("revision = %d, want 2", revision)
	}
}

func TestParseCSVRejectsInvalidReloadData(t *testing.T) {
	bad := []byte("method,port,path,query,request headers,request body,status code,response headers,response body\nGET,8081,/x,,,,20A,,\n")
	if _, err := parseCSV(bad); err == nil {
		t.Fatal("expected invalid status code to fail parsing")
	}
}
