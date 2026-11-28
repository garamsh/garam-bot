package naruworkspace

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/garamsh/naru/lib/machineapi"
)

// renewWindow is the end of a caller certificate's life in which it renews (naru ADR 0116 item 5).
const renewWindow = 30 * 24 * time.Hour

// establishCredential loads the stored pair, and registers where there is none or it has expired.
func (p *Provider) establishCredential(ctx context.Context) error {
	cred, err := p.cfg.Store.Load(ctx)
	if err != nil && !errors.Is(err, ErrNoCredential) {
		return fmt.Errorf("load the caller credential: %w", err)
	}
	if cred.Key != nil && len(cred.Certificate) > 0 {
		cert, err := x509.ParseCertificate(cred.Certificate)
		if err != nil {
			return fmt.Errorf("the stored caller certificate does not parse: %w", err)
		}
		if p.cfg.Now().Before(cert.NotAfter) {
			p.use(cred, cert)
			p.cfg.Log.Info("loaded the Machine API caller certificate", "caller", cert.Subject.CommonName, "not_after", cert.NotAfter)
			return nil
		}
		p.cfg.Log.Warn("the stored caller certificate has expired; registering again", "not_after", cert.NotAfter)
	}

	return p.register(ctx, cred.Attempted)
}

// register presents the token file's token once for a key made here, recording the attempt with
// the key before it is sent: a presented token is spent whatever the answer, so the same token is
// never presented again (naru ADR 0116 item 3).
func (p *Provider) register(ctx context.Context, attempted string) error {
	raw, err := os.ReadFile(p.cfg.TokenFile)
	if err != nil {
		// By path alone: the content is a secret.
		return fmt.Errorf("read the registration token file %s: %w", p.cfg.TokenFile, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return fmt.Errorf("the registration token file %s is empty", p.cfg.TokenFile)
	}
	digest := sha256.Sum256([]byte(token))
	attempt := hex.EncodeToString(digest[:])
	if attempt == attempted {
		return errors.New("the registration token was presented already: a new one is needed")
	}
	key, csr, err := newKey()
	if err != nil {
		return err
	}
	if err := p.cfg.Store.Save(ctx, Credential{Key: key, Attempted: attempt}); err != nil {
		return fmt.Errorf("record the registration attempt: %w", err)
	}
	answer, err := p.anon.Register(ctx, token, csr)
	if err != nil {
		// A refusal that spent nothing frees the token for another attempt: a rate limit, or a
		// signer that answers before it reads the token (naru ADR 0116 items 3 and 7).
		var refusal *machineapi.Error
		if errors.As(err, &refusal) && (refusal.Status == http.StatusTooManyRequests || refusal.Status == http.StatusConflict) {
			if err := p.cfg.Store.Save(ctx, Credential{}); err != nil {
				return fmt.Errorf("clear the registration attempt: %w", err)
			}
		}
		return translate("register as a Machine API caller", err)
	}
	cert, err := x509.ParseCertificate(answer.Certificate)
	if err != nil {
		return fmt.Errorf("the registration answered a certificate that does not parse: %w", err)
	}
	cred := Credential{Key: key, Certificate: answer.Certificate}
	if err := p.cfg.Store.Save(ctx, cred); err != nil {
		return fmt.Errorf("keep the caller certificate: %w", err)
	}
	p.use(cred, cert)
	p.cfg.Log.Info("registered as a Machine API caller", "caller", cert.Subject.CommonName, "not_after", cert.NotAfter)

	return nil
}

// renewals renews the certificate in its last 30 days until ctx ends. A refused renewal renews no
// more: the certificate then serves until it expires, and the place's operator is warned to give a
// new registration token before it does.
func (p *Provider) renewals(ctx context.Context) {
	lost := false
	for ctx.Err() == nil {
		cred, cert := p.current()
		if d := cert.NotAfter.Add(-renewWindow).Sub(p.cfg.Now()); d > 0 {
			sleep(ctx, d)
			continue
		}
		err := p.renew(ctx, cred, cert)
		var refusal *machineapi.Error
		switch {
		case err == nil:
			lost = false
		case ctx.Err() != nil:
			return
		case errors.As(err, &refusal) && refusal.Code == machineapi.CodeNotDue:
			sleep(ctx, time.Hour)
		case errors.As(err, &refusal) && refusal.Status == http.StatusForbidden:
			msg := fmt.Sprintf("the Machine API caller certificate renews no more and expires at %s: "+
				"place a new registration token before then", cert.NotAfter.UTC().Format(time.RFC3339))
			if lost {
				msg = "a renewal's answer was lost, so " + msg
			}
			p.cfg.Log.Error(msg, "error", err)
			p.cfg.Warn(msg)
			sleep(ctx, cert.NotAfter.Sub(p.cfg.Now()))
			return
		case errors.As(err, &refusal):
			p.cfg.Log.Warn("the renewal of the caller certificate was refused; trying again", "error", err)
			sleep(ctx, 10*time.Minute)
		default:
			// No answer: the renewal may have been taken, and only a second one tells.
			lost = true
			p.cfg.Log.Warn("the renewal of the caller certificate had no answer; trying again", "error", err)
			sleep(ctx, time.Minute)
		}
	}
}

// renew asks for a certificate of a new key, signed with the current pair, and keeps it before it
// is used. A Save that fails is retried: a certificate renews once, so the new pair is the one that
// renews next.
func (p *Provider) renew(ctx context.Context, cred Credential, cert *x509.Certificate) error {
	key, csr, err := newKey()
	if err != nil {
		return err
	}
	answer, err := p.api.Renew(ctx, cred.Key, cert, csr)
	if err != nil {
		return err
	}
	next, err := x509.ParseCertificate(answer.Certificate)
	if err != nil {
		return fmt.Errorf("the renewal answered a certificate that does not parse: %w", err)
	}
	renewed := Credential{Key: key, Certificate: answer.Certificate}
	p.use(renewed, next)
	for {
		err := p.cfg.Store.Save(ctx, renewed)
		if err == nil {
			break
		}
		p.cfg.Log.Error("keeping the renewed caller certificate; trying again", "error", err)
		if sleep(ctx, 10*time.Second); ctx.Err() != nil {
			return ctx.Err()
		}
	}
	p.cfg.Log.Info("renewed the Machine API caller certificate", "not_after", next.NotAfter)

	return nil
}

// use makes cred the pair every later connection presents.
func (p *Provider) use(cred Credential, cert *x509.Certificate) {
	p.credMu.Lock()
	p.cred = cred
	p.credMu.Unlock()
	p.pair.Store(&tls.Certificate{Certificate: [][]byte{cred.Certificate}, PrivateKey: cred.Key, Leaf: cert})
}

func (p *Provider) current() (Credential, *x509.Certificate) {
	p.credMu.Lock()
	defer p.credMu.Unlock()

	return p.cred, p.pair.Load().Leaf
}

// newKey makes a P-256 key and its certificate request. The signer names the certificate after the
// token or the current certificate, not the request.
func newKey() (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("make the caller key: %w", err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return nil, nil, fmt.Errorf("make the caller certificate request: %w", err)
	}

	return key, csr, nil
}
