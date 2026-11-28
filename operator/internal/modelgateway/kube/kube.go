// Package kube is the model gateway's signing key over a Kubernetes API server.
package kube

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/garamsh/garam-bot/operator/internal/modelgateway"
)

// SigningKeyData is the key of the Secret's data the signing key is held under, a PKCS #8 PEM block.
const SigningKeyData = "signing-key.pem"

// SigningKeys is the Secret Name in Namespace, which the chart creates empty. The first gateway to
// start writes a new P-256 key into it, and every later one, and every replica, reads that key
// (garam-bot ADR 0044 decision 3). The gateway gets and updates that one Secret, by name, and creates
// none: RBAC cannot bound a create by name, and a gateway that may create Secrets in its namespace can
// mint a token Secret of any ServiceAccount there (ADR 0044 decision 8).
type SigningKeys struct {
	Kube      kubernetes.Interface
	Namespace string
	Name      string
}

var _ modelgateway.SigningKeys = SigningKeys{}

func (s SigningKeys) Load(ctx context.Context) (*ecdsa.PrivateKey, error) {
	secrets := s.Kube.CoreV1().Secrets(s.Namespace)
	held, err := secrets.Get(ctx, s.Name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("the Secret %s/%s does not exist: the chart creates it empty", s.Namespace, s.Name)
	case err != nil:
		return nil, fmt.Errorf("read the Secret %s/%s: %w", s.Namespace, s.Name, err)
	case len(held.Data[SigningKeyData]) > 0:
		return parse(held, s.Namespace)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	written := held.DeepCopy()
	if written.Data == nil {
		written.Data = map[string][]byte{}
	}
	written.Data[SigningKeyData] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	// The update carries the resourceVersion read, so of two replicas writing at once, one wins.
	_, err = secrets.Update(ctx, written, metav1.UpdateOptions{})
	switch {
	case err == nil:
		return key, nil
	case apierrors.IsConflict(err):
		// Another replica wrote it first, and its key is the one every replica signs with.
		held, err := secrets.Get(ctx, s.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("read the Secret %s/%s: %w", s.Namespace, s.Name, err)
		}
		return parse(held, s.Namespace)
	default:
		return nil, fmt.Errorf("write the Secret %s/%s: %w", s.Namespace, s.Name, err)
	}
}

func parse(secret *corev1.Secret, namespace string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(secret.Data[SigningKeyData])
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("the Secret %s/%s holds no %s", namespace, secret.Name, SigningKeyData)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the Secret %s/%s's %s does not parse: %w", namespace, secret.Name, SigningKeyData, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("the signing key is no P-256 key")
	}
	return key, nil
}
