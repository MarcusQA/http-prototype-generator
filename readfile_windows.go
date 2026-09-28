//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// openCSVFile deliberately requests FILE_SHARE_DELETE in addition to normal
// read/write sharing. That allows another program to atomically rename or
// replace http_values.csv during the brief period in which we are reading it.
func openCSVFile(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	h, err := syscall.CreateFile(
		name,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}

	f := os.NewFile(uintptr(h), path)
	if f == nil {
		_ = syscall.CloseHandle(h)
		return nil, fmt.Errorf("open %q: could not create file handle", path)
	}
	return f, nil
}
