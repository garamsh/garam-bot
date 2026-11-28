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

// SigningKeys is the Secret Name in Namespace. The first gateway to start writes a new P-256 key to
// it, and every later one, and every replica, reads that key (garam-bot ADR 0044 decision 3).
type SigningKeys struct {
	Kube      kubernetes.Interface
	Namespace string
	Name      string
}

var _ modelgateway.SigningKeys = SigningKeys{}

func (s SigningKeys) Load(ctx context.Context) (*ecdsa.PrivateKey, error) {
	secrets := s.Kube.CoreV1().Secrets(s.Namespace)
	held, err := secrets.Get(ctx, s.Name, metav1.GetOptions{})
	if err == nil {
		return parse(held, s.Namespace)
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read the Secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	secret := &corev1.Secret{
		Namespace: s.Namespace, Name: s.Name,
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{SigningKeyData: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})},
	}
	_, err = secrets.Create(ctx, secret, metav1.CreateOptions{})
	switch {
	case err == nil:
		return key, nil
	case apierrors.IsAlreadyExists(err):
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
