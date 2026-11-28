package main

import (
	"fmt"
	"io"
	"os"
)

// cat copies each named file to stdout in order, and stdin for no argument or "-". It reports each
// file it cannot read on stderr, keeps going, and returns 1 if any failed.
func cat(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"-"}
	}
	status := 0
	for _, name := range args {
		if err := catOne(name, stdin, stdout); err != nil {
			fmt.Fprintf(stderr, "cat: %v\n", err)
			status = 1
		}
	}
	return status
}

func catOne(name string, stdin io.Reader, stdout io.Writer) error {
	if name == "-" {
		_, err := io.Copy(stdout, stdin)
		return err
	}
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(stdout, file)
	return err
}
