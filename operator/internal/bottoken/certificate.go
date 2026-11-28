package bottoken

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// serving is the certificate answered to every handshake. Its key exists only in this process.
type serving struct {
	mu   sync.Mutex
	cert *tls.Certificate
}

func (s *serving) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cert, nil
}

func (s *serving) set(cert *tls.Certificate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cert = cert
}

func (s *service) Serve(ctx context.Context, listener net.Listener) error {
	current, err := s.issue()
	if err != nil {
		return err
	}
	if err := s.publish(ctx, bundleOf(current)); err != nil {
		return nil
	}
	certificate := &serving{cert: current}
	server := &http.Server{
		Handler:           s.handler(),
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: certificate.get},
		ReadHeaderTimeout: readTimeout,
		// A failed handshake is most often a process that does not trust the certificate yet.
		ErrorLog: slog.NewLogLogger(s.cfg.Log.Handler(), slog.LevelWarn),
	}
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		if err := server.ServeTLS(listener, "", ""); !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})
	group.Go(func() error {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
		defer cancel()
		return server.Shutdown(shutdown)
	})
	group.Go(func() error { return s.renew(ctx, certificate, current) })
	return group.Wait()
}

// renew replaces the serving certificate once two thirds of its validity have passed. The successor is
// published beside it first, and served only after Propagation, so a bot's process trusts it before it
// is answered (T-1). Until the successor is published, the current certificate is served: a failure
// never stops the server.
func (s *service) renew(ctx context.Context, certificate *serving, current *tls.Certificate) error {
	for {
		due := current.Leaf.NotAfter.Add(-s.cfg.Validity / 3).Sub(s.cfg.Now())
		if err := sleep(ctx, due); err != nil {
			return nil
		}
		next, err := s.issue()
		if err != nil {
			// The system's randomness failing is not cured by waiting; the current certificate is kept.
			s.cfg.Log.Error("no next serving certificate", "error", err)
			return err
		}
		if err := s.publish(ctx, bundleOf(next, current)); err != nil {
			return nil
		}
		if err := sleep(ctx, s.cfg.Propagation); err != nil {
			return nil
		}
		certificate.set(next)
		s.cfg.Log.Info("serving certificate replaced", "notAfter", next.Leaf.NotAfter)
		current = next
	}
}

// The back-off between failed publishes, doubling from the first to the last.
const (
	firstPublishBackoff = time.Second
	lastPublishBackoff  = time.Minute
)

// publish writes bundle to the trust store, again after a back-off until it is written or ctx ends,
// when it answers ctx's error.
func (s *service) publish(ctx context.Context, bundle []byte) error {
	wait := firstPublishBackoff
	for {
		err := s.cfg.Trust.Publish(ctx, bundle)
		if err == nil {
			return nil
		}
		s.cfg.Log.Warn("the serving certificates were not published; publishing them again", "error", err,
			"wait", wait)
		if err := sleep(ctx, wait); err != nil {
			return err
		}
		wait = min(2*wait, lastPublishBackoff)
	}
}

// issue makes a key and a certificate it signs itself, valid for Validity under DNSNames.
func (s *service) issue() (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate the serving key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate the serving certificate's serial: %w", err)
	}
	now := s.cfg.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "garam-connector bot token"},
		DNSNames:              s.cfg.DNSNames,
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(s.cfg.Validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("sign the serving certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("read the serving certificate: %w", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// bundleOf is the PEM of the certificates a bot's process trusts.
func bundleOf(certificates ...*tls.Certificate) []byte {
	var out bytes.Buffer
	for _, certificate := range certificates {
		_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: certificate.Leaf.Raw})
	}
	return out.Bytes()
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
