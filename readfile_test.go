package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCSVFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "http_values.csv")
	want := []byte("method,port,path\nGET,8081,/ping\n")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readCSVFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("readCSVFile() = %q, want %q", got, want)
	}
}
