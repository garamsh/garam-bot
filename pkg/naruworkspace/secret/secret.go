// Package secret keeps the naru Machine provider's caller credential in one Kubernetes Secret, for a
// place that is a cluster: the key never leaves the Secret and the process.
package secret

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/garamsh/garam-bot/pkg/naruworkspace"
)

// The Secret's data keys.
const (
	keyData         = "key.pem"
	certificateData = "certificate.pem"
	attemptedData   = "registrationAttempt"
)

// Store is a naruworkspace.CredentialStore over one Secret that already exists. It never creates
// one, so its Role needs only get and update, on that Secret's name alone.
type Store struct {
	client    kubernetes.Interface
	namespace string
	name      string

	mu sync.Mutex
	// read is the Secret as last read or written here; a Save updates it on its resourceVersion.
	read *corev1.Secret
}

var _ naruworkspace.CredentialStore = (*Store)(nil)

// New returns a Store over the Secret name in namespace.
func New(client kubernetes.Interface, namespace, name string) *Store {
	return &Store{client: client, namespace: namespace, name: name}
}

// Load answers the Credential the Secret holds, or naruworkspace.ErrNoCredential where it holds no
// data. A missing Secret is an error.
func (s *Store) Load(ctx context.Context) (naruworkspace.Credential, error) {
	found, err := s.client.CoreV1().Secrets(s.namespace).Get(ctx, s.name, metav1.GetOptions{})
	if err != nil {
		return naruworkspace.Credential{}, fmt.Errorf("read the caller Secret %s/%s: %w", s.namespace, s.name, err)
	}
	s.mu.Lock()
	s.read = found
	s.mu.Unlock()
	if len(found.Data) == 0 {
		return naruworkspace.Credential{}, naruworkspace.ErrNoCredential
	}

	return decode(found.Data)
}

// Save writes c into the Secret, on the condition that nobody wrote it after the last Load or Save
// here.
func (s *Store) Save(ctx context.Context, c naruworkspace.Credential) error {
	data, err := encode(c)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read == nil {
		return errors.New("the caller Secret is saved only after it is loaded")
	}
	updated := s.read.DeepCopy()
	updated.Data = data
	written, err := s.client.CoreV1().Secrets(s.namespace).Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("write the caller Secret %s/%s: %w", s.namespace, s.name, err)
	}
	s.read = written

	return nil
}

func encode(c naruworkspace.Credential) (map[string][]byte, error) {
	data := map[string][]byte{}
	if c.Key != nil {
		der, err := x509.MarshalPKCS8PrivateKey(c.Key)
		if err != nil {
			return nil, fmt.Errorf("encode the caller key: %w", err)
		}
		data[keyData] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	}
	if len(c.Certificate) > 0 {
		data[certificateData] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate})
	}
	if c.Attempted != "" {
		data[attemptedData] = []byte(c.Attempted)
	}

	return data, nil
}

func decode(data map[string][]byte) (naruworkspace.Credential, error) {
	c := naruworkspace.Credential{Attempted: string(data[attemptedData])}
	if raw, ok := data[keyData]; ok {
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "PRIVATE KEY" {
			return naruworkspace.Credential{}, fmt.Errorf("%s holds no PEM private key", keyData)
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return naruworkspace.Credential{}, fmt.Errorf("%s: %w", keyData, err)
		}
		key, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return naruworkspace.Credential{}, fmt.Errorf("%s is not an ECDSA key", keyData)
		}
		c.Key = key
	}
	if raw, ok := data[certificateData]; ok {
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return naruworkspace.Credential{}, fmt.Errorf("%s holds no PEM certificate", certificateData)
		}
		c.Certificate = block.Bytes
	}

	return c, nil
}
