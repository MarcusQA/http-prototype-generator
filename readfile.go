package main

import "io"

// readCSVFile reads the CSV through an OS-specific open routine. On Windows,
// the file is opened with read/write/delete sharing so spreadsheet software or
// Explorer can replace/rename http_values.csv while the prototype is running.
func readCSVFile(path string) ([]byte, error) {
	f, err := openCSVFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
