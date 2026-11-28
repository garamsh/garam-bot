//go:build !linux

package main

import (
	"errors"
	"io"
)

// writeFile is the helper's on Linux alone, where it runs in a workspace: no other system offers the
// resolution with no link followed that FILE-3 needs.
func writeFile(string, io.Reader) error {
	return errors.New("the file helper writes on Linux alone")
}
