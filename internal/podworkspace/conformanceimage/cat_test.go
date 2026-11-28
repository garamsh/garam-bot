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

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestCat(t *testing.T) {
	first := writeFile(t, "first", "one\n")
	second := writeFile(t, "second", "two\n")
	missing := filepath.Join(t.TempDir(), "missing")

	tests := []struct {
		name       string
		args       []string
		wantOut    string
		wantStatus int
		wantErr    string
	}{
		{name: "copies a named file and not stdin", args: []string{first}, wantOut: "one\n"},
		{name: "copies files in order", args: []string{first, second}, wantOut: "one\ntwo\n"},
		{name: "copies stdin when given no argument", wantOut: "from stdin"},
		{name: "copies stdin for a dash between files", args: []string{first, "-", second}, wantOut: "one\nfrom stdintwo\n"},
		{name: "reports a missing file and goes on", args: []string{missing, second}, wantOut: "two\n", wantStatus: 1, wantErr: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			status := cat(tt.args, strings.NewReader("from stdin"), &stdout, &stderr)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, tt.wantOut, stdout.String())
			if tt.wantErr == "" {
				assert.Empty(t, stderr.String())
			} else {
				assert.Contains(t, stderr.String(), tt.wantErr)
			}
		})
	}
}
