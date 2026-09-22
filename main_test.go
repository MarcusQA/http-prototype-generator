package main

import (
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
