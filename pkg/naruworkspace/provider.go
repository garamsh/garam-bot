package naruworkspace

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/garamsh/naru/lib/machineapi"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// requestTimeout bounds one Machine API call. An exec runs up to 600 seconds, and orch answers it
// within 12 minutes (naru ADR 0118, Consequences).
const requestTimeout = 12 * time.Minute

// Config is what the place decides about the naru fleet its workspaces run on.
type Config struct {
	// URL is orch's Machine API, an https URL without the version path.
	URL string
	// Roots verify orch's certificate. Nothing else is trusted for it.
	Roots *x509.CertPool
	// TokenFile holds the one-time registration token, read only while the provider holds no
	// certificate. It is never logged.
	TokenFile string
	// Store keeps the caller's key and certificate at the place.
	Store CredentialStore
	// ProxyAddress is naru-proxy's host:port, which Dial reaches a Machine's port through.
	ProxyAddress string
	// ProxyRoots verify naru-proxy's certificate. Nil is the system's roots.
	ProxyRoots *x509.CertPool
	// RootDiskBytes is the root disk of every Machine, the place's configuration (README §Mapping
	// onto naru's Machine API).
	RootDiskBytes uint64
	// Warn reports what the place's operator must act on: a certificate that renews no more.
	Warn func(message string)
	// Log receives what the provider does. It is never given a key, a certificate or a token.
	Log *slog.Logger
	// FollowEvery is how long the provider waits after a page of the event log that held no event.
	// Zero is one second.
	FollowEvery time.Duration
	// Now is the provider's clock. Nil is time.Now.
	Now func() time.Time
}

// Provider implements v1alpha1.Provider over naru's Machine API, as one caller. Establish it
// before any other call, and Run it to follow the Machines' status and renew its certificate.
type Provider struct {
	cfg  Config
	api  *machineapi.Client
	anon *machineapi.Client
	pair atomic.Pointer[tls.Certificate]

	credMu sync.Mutex
	cred   Credential

	mu     sync.Mutex
	record map[v1alpha1.Name]*entry
	cursor string
}

var _ v1alpha1.Provider = (*Provider)(nil)

// entry is what the provider learned of one Machine: its id, which a later Machine of the name does
// not share, its spec as a read answers it, and its status.
type entry struct {
	id     string
	spec   v1alpha1.Spec
	status machineapi.Status
}

// New returns a Provider for the fleet cfg names. It calls nothing: Establish does.
func New(cfg Config) (*Provider, error) {
	switch {
	case cfg.Roots == nil:
		return nil, errors.New("naruworkspace: the roots orch's certificate chains to are required")
	case cfg.TokenFile == "":
		return nil, errors.New("naruworkspace: a registration token file is required")
	case cfg.Store == nil:
		return nil, errors.New("naruworkspace: a credential store is required")
	case cfg.RootDiskBytes == 0:
		return nil, errors.New("naruworkspace: the root disk size is required")
	case cfg.Warn == nil || cfg.Log == nil:
		return nil, errors.New("naruworkspace: a warning hook and a logger are required")
	}
	if _, _, err := net.SplitHostPort(cfg.ProxyAddress); err != nil {
		return nil, fmt.Errorf("naruworkspace: naru-proxy's address %q is not host:port", cfg.ProxyAddress)
	}
	if cfg.FollowEvery == 0 {
		cfg.FollowEvery = time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	p := &Provider{cfg: cfg, record: map[v1alpha1.Name]*entry{}}
	var err error
	if p.api, err = machineapi.New(cfg.URL, p.httpClient(true)); err != nil {
		return nil, fmt.Errorf("naruworkspace: %w", err)
	}
	if p.anon, err = machineapi.New(cfg.URL, p.httpClient(false)); err != nil {
		return nil, fmt.Errorf("naruworkspace: %w", err)
	}

	return p, nil
}

// httpClient verifies orch against cfg.Roots alone, and presents the caller's current certificate
// when withCert is set, so a renewal takes effect on the next connection without a new client.
func (p *Provider) httpClient(withCert bool) *http.Client {
	conf := &tls.Config{RootCAs: p.cfg.Roots, MinVersion: tls.VersionTLS13}
	if withCert {
		conf.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			if pair := p.pair.Load(); pair != nil {
				return pair, nil
			}
			return &tls.Certificate{}, nil
		}
	}

	return &http.Client{
		Timeout:   requestTimeout,
		Transport: &http.Transport{TLSClientConfig: conf, Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Establish loads the caller's credential, registering once with the token file's token where it
// holds none, and lists the Machines it has. An error is the place's to stop on.
func (p *Provider) Establish(ctx context.Context) error {
	if err := p.establishCredential(ctx); err != nil {
		return err
	}

	return p.list(ctx)
}

// Run follows the event log for the Machines' status and renews the certificate in its last 30
// days, until ctx ends.
func (p *Provider) Run(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.renewals(ctx)
	}()
	p.follow(ctx)
	<-done

	return ctx.Err()
}

// Capabilities reports what README §Mapping onto naru's Machine API states: snapshots of the
// volume, egress rules, and no exec input.
func (p *Provider) Capabilities(context.Context) (v1alpha1.Capabilities, error) {
	return v1alpha1.Capabilities{Snapshot: true, Egress: true, Stdin: false, Labels: false}, nil
}

// list replaces the record with the Machines orch lists, and follows the log from the cursor the
// list is current at.
func (p *Provider) list(ctx context.Context) error {
	l, err := p.api.Machines(ctx)
	if err != nil {
		return translate("list the Machines", err)
	}
	record := make(map[v1alpha1.Name]*entry, len(l.Machines))
	for _, m := range l.Machines {
		record[v1alpha1.Name(m.Name)] = entryOf(m)
	}
	p.mu.Lock()
	p.record, p.cursor = record, l.Cursor
	p.mu.Unlock()

	return nil
}

func entryOf(m machineapi.Machine) *entry {
	return &entry{id: m.ID, spec: workspaceSpec(m.Spec), status: m.Status}
}

// follow reads the event log from the cursor, applying each event, until ctx ends (README §Mapping
// onto naru's Machine API, "Status is followed, not polled"). A cursor older than the log keeps is
// answered by listing again.
func (p *Provider) follow(ctx context.Context) {
	wait := p.cfg.FollowEvery
	for ctx.Err() == nil {
		p.mu.Lock()
		cursor := p.cursor
		p.mu.Unlock()
		page, err := p.api.Events(ctx, cursor)
		switch {
		case isCode(err, machineapi.CodeCursorExpired):
			p.cfg.Log.Info("the event log no longer holds the cursor; listing the Machines again")
			err = p.list(ctx)
		case err == nil:
			p.apply(ctx, page)
			if len(page.Events) > 0 {
				wait = p.cfg.FollowEvery
				continue
			}
		}
		if err != nil && ctx.Err() == nil {
			p.cfg.Log.Warn("following the Machine API's event log", "error", err)
			wait = min(2*wait, time.Minute)
		}
		sleep(ctx, wait)
		if err == nil {
			wait = p.cfg.FollowEvery
		}
	}
}

// apply learns each event of page in order, then moves the cursor past them. A status of a Machine
// the record does not hold, or holds under another id, is read whole, since an event carries no spec.
func (p *Provider) apply(ctx context.Context, page machineapi.EventPage) {
	for _, ev := range page.Events {
		name := v1alpha1.Name(ev.Machine)
		p.mu.Lock()
		e := p.record[name]
		known := e != nil && e.id == ev.ID
		switch {
		case ev.Type == machineapi.EventDeleted && known:
			delete(p.record, name)
		case ev.Type == machineapi.EventStatus && known:
			e.status = ev.Status
		}
		p.mu.Unlock()
		if ev.Type != machineapi.EventStatus || known {
			continue
		}
		m, err := p.api.Machine(ctx, ev.Machine)
		p.mu.Lock()
		switch {
		case err == nil:
			p.record[name] = entryOf(m)
		case isCode(err, machineapi.CodeNotFound):
			delete(p.record, name)
		default:
			p.cfg.Log.Warn("reading a Machine an event named", "machine", ev.Machine, "error", err)
		}
		p.mu.Unlock()
	}
	p.mu.Lock()
	p.cursor = page.Cursor
	p.mu.Unlock()
}

// learn records what a write answered: the Machine whole where the record does not hold it under
// its id, and otherwise its status where the write moved its generation past the record's. A
// change of phase alone is the event log's to tell.
func (p *Provider) learn(m machineapi.Machine) v1alpha1.Workspace {
	name := v1alpha1.Name(m.Name)
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.record[name]
	switch {
	case e == nil || e.id != m.ID:
		e = entryOf(m)
		p.record[name] = e
	case m.Status.Generation > e.status.Generation:
		e.status = m.Status
	}

	return workspace(name, e)
}

func workspace(name v1alpha1.Name, e *entry) v1alpha1.Workspace {
	return v1alpha1.Workspace{
		Name:   name,
		Spec:   cloneSpec(e.spec),
		Status: v1alpha1.Status{Phase: phase(e.status.Phase), Reason: e.status.Reason},
	}
}

// cloneSpec copies spec, so no answer shares the record's maps and slices.
func cloneSpec(spec v1alpha1.Spec) v1alpha1.Spec {
	spec.Env = maps.Clone(spec.Env)
	spec.Ports = slices.Clone(spec.Ports)
	spec.Egress = slices.Clone(spec.Egress)
	for i := range spec.Egress {
		spec.Egress[i].Ports = slices.Clone(spec.Egress[i].Ports)
	}

	return spec
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
