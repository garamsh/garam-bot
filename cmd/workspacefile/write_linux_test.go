package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRun_WriteOnAKernelWithoutOpenat2ExitsUnsupportedAndWritesNothing(t *testing.T) {
	dir := realDir(t)
	path := filepath.Join(dir, "token")
	t.Cleanup(func() { openat2 = unix.Openat2 })
	openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS }

	var out, errOut bytes.Buffer
	assert.Equal(t, exitUnsupported, run([]string{"write", path}, strings.NewReader("token"), &out, &errOut))
	assert.Contains(t, errOut.String(), "Linux 5.6", "the refusal says what the node lacks")
	_, err := os.Lstat(path)
	assert.ErrorIs(t, err, os.ErrNotExist, "nothing is written by another way")

	// Control: with openat2 offered, the same write is taken.
	openat2 = unix.Openat2
	require.Equal(t, 0, run([]string{"write", path}, strings.NewReader("token"), &out, &errOut), errOut.String())
}
