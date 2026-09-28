//go:build !windows

package main

import "os"

func openCSVFile(path string) (*os.File, error) {
	return os.Open(path)
}
