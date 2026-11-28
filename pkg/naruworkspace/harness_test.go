package naruworkspace_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/garamsh/naru/lib/machineapi"
	"github.com/garamsh/naru/lib/machineapi/machineapifake"

	"github.com/garamsh/garam-bot/pkg/naruworkspace"
)

// zone is the zone the fake's stable names are under.
const zone = "example.test"

// callerName is the caller the fleet's registration token names.
const callerName = "garam-bot"

// fleet is a naru fleet for the provider's tests: machineapifake in place of orch, reached through a
// gate the test can close, and a stand-in for naru-proxy.
type fleet struct {
	t      *testing.T
	server *machineapifake.Server
	gate   *gate
	roots  *x509.CertPool
	proxy  *proxyDouble
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	server := machineapifake.Start(t, machineapifake.Options{Zone: zone, Latency: 50 * time.Millisecond, Exec: conformanceImage})
	u, err := url.Parse(server.URL)
	require.NoError(t, err)
	f := &fleet{t: t, server: server, gate: newGate(t, u.Host), roots: serverRoots(t, u.Host)}
	f.proxy = newProxyDouble(t, server.Client(callerName))

	return f
}

// config is the provider's configuration on this fleet, with a token for callerName in a file and
// store as its credential store.
func (f *fleet) config(store naruworkspace.CredentialStore, warn func(string)) naruworkspace.Config {
	f.t.Helper()
	tokenFile := filepath.Join(f.t.TempDir(), "token")
	require.NoError(f.t, os.WriteFile(tokenFile, []byte(f.server.Token(callerName)+"\n"), 0o600))
	if warn == nil {
		warn = func(string) {}
	}

	return naruworkspace.Config{
		URL:           "https://" + f.gate.addr,
		Roots:         f.roots,
		TokenFile:     tokenFile,
		Store:         store,
		ProxyAddress:  f.proxy.addr,
		ProxyRoots:    f.proxy.roots,
		RootDiskBytes: 1 << 30,
		Warn:          warn,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		FollowEvery:   20 * time.Millisecond,
	}
}

// run establishes a provider of cfg and runs it until the test ends.
func run(t *testing.T, cfg naruworkspace.Config) *naruworkspace.Provider {
	t.Helper()
	p, err := naruworkspace.New(cfg)
	require.NoError(t, err)
	require.NoError(t, p.Establish(t.Context()))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	return p
}

// conformanceImage is the conformance image of api/workspace/v1alpha1/conformance.md as the fake's
// guest: true, false, and cat of the files it names.
func conformanceImage(command []string, files map[string][]byte) machineapi.ExecResult {
	out := machineapi.ExecResult{Stdout: []byte{}, Stderr: []byte{}}
	switch command[0] {
	case "true":
	case "false":
		out.ExitCode = 1
	case "cat":
		for _, p := range command[1:] {
			b, ok := files[p]
			if !ok {
				out.ExitCode, out.Stderr = 1, fmt.Appendf(out.Stderr, "cat: %s: No such file or directory\n", p)
				continue
			}
			out.Stdout = append(out.Stdout, b...)
		}
	default:
		out.ExitCode, out.Stderr = 127, fmt.Appendf(out.Stderr, "%s: not found\n", command[0])
	}

	return out
}

// memoryStore is a CredentialStore in memory.
type memoryStore struct {
	mu    sync.Mutex
	cred  naruworkspace.Credential
	saves []naruworkspace.Credential
}

func (s *memoryStore) Load(context.Context) (naruworkspace.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cred.Key == nil && s.cred.Attempted == "" {
		return naruworkspace.Credential{}, naruworkspace.ErrNoCredential
	}

	return s.cred, nil
}

func (s *memoryStore) Save(_ context.Context, c naruworkspace.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cred = c
	s.saves = append(s.saves, c)

	return nil
}

func (s *memoryStore) stored() naruworkspace.Credential {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.cred
}

// serverRoots trusts the certificate the server at addr presents, read once: the fake's own.
func serverRoots(t *testing.T, addr string) *x509.CertPool {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // reads the test server's certificate to trust it
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	roots := x509.NewCertPool()
	roots.AddCert(conn.ConnectionState().PeerCertificates[0])

	return roots
}

// gate forwards TCP to the fake, and while closed drops every connection and takes none, so a test
// shows what the provider answers without reaching orch.
type gate struct {
	addr string
	to   string

	mu     sync.Mutex
	closed bool
	conns  []net.Conn
}

func newGate(t *testing.T, to string) *gate {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	g := &gate{addr: l.Addr().String(), to: to}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go g.forward(c)
		}
	}()

	return g
}

func (g *gate) forward(c net.Conn) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		_ = c.Close()
		return
	}
	up, err := net.Dial("tcp", g.to)
	if err != nil {
		g.mu.Unlock()
		_ = c.Close()
		return
	}
	g.conns = append(g.conns, c, up)
	g.mu.Unlock()
	go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
	_, _ = io.Copy(c, up)
	_ = c.Close()
}

// close drops every connection through the gate and refuses new ones until open.
func (g *gate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for _, c := range g.conns {
		_ = c.Close()
	}
	g.conns = nil
}

func (g *gate) open() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = false
}

// proxyDouble stands in for naru-proxy: it terminates TLS with a certificate for *.<zone> and, for
// an SNI that is the stable name of the tcp service of a running Machine of the caller, echoes what it is sent, as the
// conformance image does on its port. Any other SNI is closed.
type proxyDouble struct {
	addr   string
	roots  *x509.CertPool
	caller *machineapi.Client
}

func newProxyDouble(t *testing.T, caller *machineapi.Client) *proxyDouble {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "*." + zone}, DNSNames: []string{"*." + zone},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}},
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	p := &proxyDouble{addr: l.Addr().String(), roots: roots, caller: caller}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go p.serve(c)
		}
	}()

	return p
}

func (p *proxyDouble) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	conn, ok := c.(*tls.Conn)
	if !ok || conn.HandshakeContext(context.Background()) != nil {
		return
	}
	if !p.routes(conn.ConnectionState().ServerName) {
		return
	}
	_, _ = io.Copy(conn, conn)
}

// routes reports whether sni is m-<service>--<id>.<zone> of a running Machine's tcp service.
func (p *proxyDouble) routes(sni string) bool {
	list, err := p.caller.Machines(context.Background())
	if err != nil {
		return false
	}
	for _, m := range list.Machines {
		if m.Status.Phase != machineapi.PhaseRunning {
			continue
		}
		if slices.ContainsFunc(m.Spec.Services, func(s machineapi.Service) bool {
			return s.Protocol == machineapi.ServiceTCP && sni == machineapi.StableName(m.ID, s.Name, zone)
		}) {
			return true
		}
	}

	return false
}
