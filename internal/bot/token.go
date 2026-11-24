package bot

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"time"
)

// TokenFile is the bot's garam token, read from a file that its writer replaces while the bot runs.
// The value never reaches a log line, an error or the status file.
type TokenFile struct {
	path string

	mu     sync.Mutex
	value  []byte
	readAt time.Time
	stale  bool
}

// ReadTokenFile reads the token at path. It fails, wrapping ErrTokenUnreadable, on a file it cannot
// read or one that is empty after trimming whitespace.
func ReadTokenFile(path string, now time.Time) (*TokenFile, error) {
	value, err := readToken(path)
	if err != nil {
		return nil, err
	}

	return &TokenFile{path: path, value: value, readAt: now}, nil
}

// Token returns the token last read.
func (f *TokenFile) Token() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return string(f.value)
}

// Refresh reads the file again. It reports whether the token changed. A failed read keeps the token
// last read, marks it stale until a read succeeds, and returns the error.
func (f *TokenFile) Refresh(now time.Time) (changed bool, err error) {
	value, err := readToken(f.path)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.stale = true

		return false, err
	}
	changed = !bytes.Equal(value, f.value)
	f.value, f.readAt, f.stale = value, now, false

	return changed, nil
}

// Status returns when the token was last read and whether the last read failed.
func (f *TokenFile) Status() TokenStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	return TokenStatus{ReadAt: f.readAt, Stale: f.stale}
}

func readToken(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// The path error names the file and the failure, never its content.
		return nil, fmt.Errorf("%w: %w", ErrTokenUnreadable, err)
	}
	value := bytes.TrimSpace(data)
	if len(value) == 0 {
		return nil, fmt.Errorf("%w: %s is empty", ErrTokenUnreadable, path)
	}

	return value, nil
}
