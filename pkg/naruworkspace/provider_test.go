package naruworkspace_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/naru/lib/machineapi"
	"github.com/garamsh/naru/lib/machineapi/machineapifake"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/pkg/naruworkspace"
)

func workspaceSpec() v1alpha1.Spec {
	return v1alpha1.Spec{
		Image:     image,
		Resources: v1alpha1.Resources{CPU: 1, MemoryBytes: 256 << 20, DiskBytes: 1 << 30},
		Env:       map[string]string{"GREETING": "hello"},
		Ports:     []uint16{7},
	}
}

// waitPhase waits for Get to answer phase.
func waitPhase(t *testing.T, p v1alpha1.Provider, name v1alpha1.Name, phase v1alpha1.Phase) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		ws, err := p.Get(t.Context(), name)
		require.NoError(c, err)
		assert.Equal(c, phase, ws.Status.Phase)
	}, 10*time.Second, 10*time.Millisecond)
}

func TestEstablish_RegistersOnceWithTheTokenAndKeepsThePair(t *testing.T) {
	f := newFleet(t)
	store := &memoryStore{}
	run(t, f.config(store, nil))

	require.NotEmpty(t, store.saves)
	first := store.saves[0]
	assert.NotEmpty(t, first.Attempted, "the attempt is recorded before the token is sent")
	assert.Empty(t, first.Certificate, "no certificate is held while the token is out")
	kept := store.stored()
	require.NotNil(t, kept.Key)
	cert, err := x509.ParseCertificate(kept.Certificate)
	require.NoError(t, err)
	assert.Equal(t, callerName, cert.Subject.CommonName, "the token names the caller")
	assert.True(t, cert.PublicKey.(*ecdsa.PublicKey).Equal(&kept.Key.PublicKey), "the certificate is of the key made here")
	assert.Same(t, first.Key, kept.Key, "the key recorded with the attempt is the one registered")
	assert.Empty(t, kept.Attempted)
}

func TestEstablish_LoadsTheStoredPairWithoutReadingTheToken(t *testing.T) {
	f := newFleet(t)
	store := &memoryStore{}
	run(t, f.config(store, nil))

	cfg := f.config(store, nil)
	require.NoError(t, os.Remove(cfg.TokenFile))
	p, err := naruworkspace.New(cfg)
	require.NoError(t, err)
	require.NoError(t, p.Establish(t.Context()), "a stored pair needs no token")
	_, err = p.List(t.Context())
	require.NoError(t, err)
}

func TestEstablish_NeverPresentsATokenTwice(t *testing.T) {
	f := newFleet(t)
	store := &memoryStore{}
	cfg := f.config(store, nil)
	p, err := naruworkspace.New(cfg)
	require.NoError(t, err)
	f.gate.close()
	require.Error(t, p.Establish(t.Context()), "orch is out of reach, so the registration has no answer")
	require.NotEmpty(t, store.stored().Attempted)
	f.gate.open()

	p, err = naruworkspace.New(cfg)
	require.NoError(t, err)
	err = p.Establish(t.Context())
	require.ErrorContains(t, err, "presented already", "the same token after a lost answer")

	// control: a new token registers
	fresh := f.config(store, nil)
	p, err = naruworkspace.New(fresh)
	require.NoError(t, err)
	require.NoError(t, p.Establish(t.Context()))
	assert.NotEmpty(t, store.stored().Certificate)
}

func TestGet_AnswersFromTheRecordWithoutReachingOrch(t *testing.T) {
	f := newFleet(t)
	p := run(t, f.config(&memoryStore{}, nil))
	_, _, err := p.Create(t.Context(), "reads", workspaceSpec())
	require.NoError(t, err)
	waitPhase(t, p, "reads", v1alpha1.PhaseRunning)

	f.gate.close()
	defer f.gate.open()
	ws, err := p.Get(t.Context(), "reads")
	require.NoError(t, err, "Get calls nothing")
	assert.Equal(t, v1alpha1.PhaseRunning, ws.Status.Phase)
	assert.Equal(t, map[string]string{"GREETING": v1alpha1.Redacted}, ws.Spec.Env)
	list, err := p.List(t.Context())
	require.NoError(t, err)
	assert.Len(t, list, 1)
	// control: a call that reaches orch fails while the gate is closed
	_, err = p.Start(t.Context(), "reads")
	require.Error(t, err)
}

func TestRun_FollowsStatusFromTheEventLog(t *testing.T) {
	f := newFleet(t)
	p := run(t, f.config(&memoryStore{}, nil))
	_, _, err := p.Create(t.Context(), "followed", workspaceSpec())
	require.NoError(t, err)
	waitPhase(t, p, "followed", v1alpha1.PhaseRunning)

	// Another client of the same caller stops it: only the event log tells the provider.
	other := f.server.Client(callerName)
	_, err = other.StopMachine(t.Context(), "followed", machineapi.StopRequest{})
	require.NoError(t, err)
	waitPhase(t, p, "followed", v1alpha1.PhaseStopped)

	// A cursor the log no longer holds is answered by listing again: the provider is cut off while
	// the Machine starts and the log drops those events.
	f.gate.close()
	_, err = other.StartMachine(t.Context(), "followed")
	require.NoError(t, err)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		m, err := other.Machine(t.Context(), "followed")
		require.NoError(c, err)
		assert.Equal(c, machineapi.PhaseRunning, m.Status.Phase)
	}, 10*time.Second, 10*time.Millisecond)
	f.server.ExpireLog(callerName)
	f.gate.open()
	waitPhase(t, p, "followed", v1alpha1.PhaseRunning)

	require.NoError(t, other.DeleteMachine(t.Context(), "followed"))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := p.Get(t.Context(), "followed")
		assert.ErrorIs(c, err, v1alpha1.ErrNotFound)
	}, 10*time.Second, 10*time.Millisecond)
}

func TestEstablish_RebuildsTheRecordFromTheMachines(t *testing.T) {
	f := newFleet(t)
	store := &memoryStore{}
	first := run(t, f.config(store, nil))
	spec := workspaceSpec()
	_, _, err := first.Create(t.Context(), "kept", spec)
	require.NoError(t, err)
	waitPhase(t, first, "kept", v1alpha1.PhaseRunning)

	second, err := naruworkspace.New(f.config(store, nil))
	require.NoError(t, err)
	require.NoError(t, second.Establish(t.Context()))
	ws, err := second.Get(t.Context(), "kept")
	require.NoError(t, err)
	want := spec
	want.Env = map[string]string{"GREETING": v1alpha1.Redacted}
	assert.True(t, v1alpha1.SameSpec(want, ws.Spec), "the spec as created, its env redacted: %+v", ws.Spec)
	assert.Equal(t, v1alpha1.PhaseRunning, ws.Status.Phase)
}

func TestRun_RenewsAPairInItsLastThirtyDays(t *testing.T) {
	f := newFleet(t)
	_, key, cert := f.server.ClientWithPair(callerName, machineapifake.CallerRole)
	store := &memoryStore{cred: naruworkspace.Credential{Key: key, Certificate: cert.Raw}}
	var warnings warnings
	p := run(t, f.config(store, warnings.add))

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NotEqual(c, cert.Raw, store.stored().Certificate)
	}, 10*time.Second, 10*time.Millisecond)
	renewed := store.stored()
	assert.False(t, renewed.Key.Equal(key), "the renewal is for a new key")
	next, err := x509.ParseCertificate(renewed.Certificate)
	require.NoError(t, err)
	assert.Equal(t, callerName, next.Subject.CommonName)
	_, _, err = p.Create(t.Context(), "after-renewal", workspaceSpec())
	require.NoError(t, err, "the renewed pair is presented")
	assert.Empty(t, warnings.all())
}

func TestRun_WarnsWhenThePairRenewsNoMore(t *testing.T) {
	f := newFleet(t)
	caller, key, cert := f.server.ClientWithPair(callerName, machineapifake.CallerRole)
	// The pair renews once, here: the provider's renewal then answers as after a lost answer.
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, other)
	require.NoError(t, err)
	_, err = caller.Renew(t.Context(), key, cert, csr)
	require.NoError(t, err)

	store := &memoryStore{cred: naruworkspace.Credential{Key: key, Certificate: cert.Raw}}
	var warnings warnings
	run(t, f.config(store, warnings.add))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Len(c, warnings.all(), 1)
	}, 10*time.Second, 10*time.Millisecond)
	assert.Contains(t, warnings.all()[0], "renews no more")
	assert.Equal(t, cert.Raw, store.stored().Certificate, "the pair stays until it expires")
}

func TestNew_RefusesAnIncompleteConfiguration(t *testing.T) {
	f := newFleet(t)
	complete := f.config(&memoryStore{}, nil)
	_, err := naruworkspace.New(complete)
	require.NoError(t, err, "control: the complete configuration")
	for name, breakIt := range map[string]func(*naruworkspace.Config){
		"no URL":           func(c *naruworkspace.Config) { c.URL = "" },
		"an http URL":      func(c *naruworkspace.Config) { c.URL = "http://" + f.gate.addr },
		"no roots":         func(c *naruworkspace.Config) { c.Roots = nil },
		"no token file":    func(c *naruworkspace.Config) { c.TokenFile = "" },
		"no store":         func(c *naruworkspace.Config) { c.Store = nil },
		"no proxy address": func(c *naruworkspace.Config) { c.ProxyAddress = "" },
		"no root disk":     func(c *naruworkspace.Config) { c.RootDiskBytes = 0 },
		"no warning hook":  func(c *naruworkspace.Config) { c.Warn = nil },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := complete
			breakIt(&cfg)
			_, err := naruworkspace.New(cfg)
			require.Error(t, err)
		})
	}
}

func TestDial_SendsTheServiceNameOfThePort(t *testing.T) {
	f := newFleet(t)
	p := run(t, f.config(&memoryStore{}, nil))
	spec := workspaceSpec()
	spec.Ports = []uint16{7, 8}
	_, _, err := p.Create(t.Context(), "dialled", spec)
	require.NoError(t, err)
	waitPhase(t, p, "dialled", v1alpha1.PhaseRunning)
	m, err := f.server.Client(callerName).Machine(t.Context(), "dialled")
	require.NoError(t, err)

	for _, port := range []uint16{7, 8} {
		conn, err := p.Dial(t.Context(), "dialled", port)
		require.NoError(t, err)
		tlsConn, ok := conn.(*tls.Conn)
		require.True(t, ok, "Dial answers the TLS connection to naru-proxy")
		assert.Equal(t, fmt.Sprintf("p%d--%s", port, m.Status.Address.Name), tlsConn.ConnectionState().ServerName)
		_ = conn.Close()
	}
}

type warnings struct {
	mu   sync.Mutex
	list []string
}

func (w *warnings) add(msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.list = append(w.list, msg)
}

func (w *warnings) all() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]string(nil), w.list...)
}
