package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/http/httpguts"
)

// ProviderKey answers the organization's own key for the provider, the one every forwarded call
// carries (KEY-1). It never answers an empty key: a gateway with none serves no model call (KEY-2).
type ProviderKey interface {
	Current() string
}

// maxProviderKey bounds the key file read, as a provider's key is a few hundred bytes.
const maxProviderKey = 4 << 10

// KeyFile is a ProviderKey read from a file, as a mounted Secret's key is, and read again while
// Watch runs. A read that fails, is empty or is no header value keeps the key read before it, so no
// call is ever forwarded without one.
type KeyFile struct {
	path string
	key  atomic.Pointer[string]
}

// NewKeyFile reads path, refusing a file that cannot be read, is empty, or holds no header value.
func NewKeyFile(path string) (*KeyFile, error) {
	k := &KeyFile{path: path}
	key, err := k.read()
	if err != nil {
		return nil, err
	}
	k.key.Store(&key)
	return k, nil
}

// Current answers the last key read that was good.
func (k *KeyFile) Current() string { return *k.key.Load() }

// Watch reads the file every interval until ctx ends, and takes what it reads where it is good. A
// read that is not is logged, without its content, and changes nothing.
func (k *KeyFile) Watch(ctx context.Context, every time.Duration, log *slog.Logger) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		key, err := k.read()
		if err != nil {
			log.Warn("the provider key file could not be read again; the key read before it is kept", "error", err)
			continue
		}
		if key != k.Current() {
			k.key.Store(&key)
			log.Info("the provider key was read again and changed")
		}
	}
}

// read reads the key, trimmed of space. Neither the key nor the file's content reaches an error.
func (k *KeyFile) read() (string, error) {
	file, err := os.Open(k.path)
	if err != nil {
		return "", fmt.Errorf("open the provider key file: %w", errors.Unwrap(err))
	}
	defer func() { _ = file.Close() }()
	read, err := io.ReadAll(io.LimitReader(file, maxProviderKey+1))
	switch {
	case err != nil:
		return "", errors.New("the provider key file cannot be read")
	case len(read) > maxProviderKey:
		return "", fmt.Errorf("the provider key file holds more than %d bytes", maxProviderKey)
	}
	key := strings.TrimSpace(string(read))
	switch {
	case key == "":
		return "", errors.New("the provider key file is empty")
	case !httpguts.ValidHeaderFieldValue(key):
		return "", errors.New("the provider key file holds no header value")
	}
	return key, nil
}

// StaticKey is a ProviderKey that never changes.
type StaticKey string

// Current answers the key.
func (k StaticKey) Current() string { return string(k) }
