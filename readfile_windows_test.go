//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// This specifically verifies the Windows behavior we need: while the app has
// the CSV open for reading, another process may rename/replace that path.
func TestOpenCSVFileAllowsRenameWhileOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "http_values.csv")
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := openCSVFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	renamed := filepath.Join(dir, "old-http_values.csv")
	if err := os.Rename(path, renamed); err != nil {
		t.Fatalf("rename while CSV handle is open failed: %v", err)
	}

	if err := os.WriteFile(path, []byte("replacement"), 0o644); err != nil {
		t.Fatalf("write replacement CSV failed: %v", err)
	}
}
