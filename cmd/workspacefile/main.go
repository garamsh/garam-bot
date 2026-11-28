// Command workspacefile is the file helper of the Pod workspace provider: "workspacefile read
// <path>" writes the file to stdout, "workspacefile write <path>" replaces the file with stdin, and
// "workspacefile install <dir>" copies the helper into dir. The provider runs it in the workspace
// through exec, so no image has to carry a shell or cat for the file calls (interface 3, FILE-1).
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// The exit codes a caller reads. Any other failure exits 1.
const (
	exitFailure      = 1
	exitFileNotFound = 3
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in io.Reader, out, errOut io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(errOut, "usage: workspacefile read <path> | write <path> | install <dir>")
		return exitFailure
	}
	var err error
	switch args[0] {
	case "read":
		err = readFile(args[1], out)
	case "write":
		err = writeFile(args[1], in)
	case "install":
		err = install(args[1])
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if err == nil {
		return 0
	}
	fmt.Fprintln(errOut, err)
	if errors.Is(err, fs.ErrNotExist) && args[0] == "read" {
		return exitFileNotFound
	}

	return exitFailure
}

func readFile(path string, out io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(out, f)

	return err
}

// writeFile replaces the file at path with in, whole: it writes a temporary file beside it and
// renames it over, so a reader sees the old content or the new and never part of either. A file
// that exists keeps its mode, and a new one is 0644.
func writeFile(path string, in io.Reader) error {
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

// install copies this executable into dir under its own name.
func install(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, filepath.Base(self)), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}

	return dst.Close()
}
