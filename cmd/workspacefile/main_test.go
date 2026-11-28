package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_WriteThenRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")

	var out, errOut bytes.Buffer
	require.Equal(t, 0, run([]string{"write", path}, strings.NewReader("a longer first content\n"), &out, &errOut), errOut.String())
	require.Equal(t, 0, run([]string{"write", path}, strings.NewReader("second\n"), &out, &errOut), errOut.String())
	require.Equal(t, 0, run([]string{"read", path}, nil, &out, &errOut), errOut.String())

	assert.Equal(t, "second\n", out.String(), "the second write replaces the first whole")
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file stays behind")
}

func TestRun_WriteKeepsAnExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o755))

	var out, errOut bytes.Buffer
	require.Equal(t, 0, run([]string{"write", path}, strings.NewReader("new"), &out, &errOut), errOut.String())

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

func TestRun_ReadOfAMissingFileExitsFileNotFound(t *testing.T) {
	var out, errOut bytes.Buffer
	assert.Equal(t, exitFileNotFound, run([]string{"read", filepath.Join(t.TempDir(), "absent")}, nil, &out, &errOut))
}

func TestRun_WriteIntoAMissingDirectoryFails(t *testing.T) {
	var out, errOut bytes.Buffer
	assert.Equal(t, exitFailure, run([]string{"write", filepath.Join(t.TempDir(), "no", "f")}, strings.NewReader("x"), &out, &errOut))
}

func TestRun_UsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"read"}, {"frob", "x"}, {"read", "a", "b"}} {
		var out, errOut bytes.Buffer
		assert.Equal(t, exitFailure, run(args, nil, &out, &errOut), "%v", args)
	}
}
