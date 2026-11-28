package naruworkspace

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"slices"

	"github.com/garamsh/naru/lib/machineapi"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// Exec runs req.Command in the running Machine. The Machine API takes no input, so a non-empty
// Stdin is refused (EXEC-2); a command ended at its timeout or by a signal is a result, its exit
// code 128 plus the signal (naru ADR 0118 item 4).
func (p *Provider) Exec(ctx context.Context, name v1alpha1.Name, req v1alpha1.ExecRequest) (v1alpha1.ExecResult, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.ExecResult{}, err
	}
	if len(req.Command) == 0 {
		return v1alpha1.ExecResult{}, fmt.Errorf("%w: an empty command", v1alpha1.ErrInvalid)
	}
	if len(req.Stdin) > 0 {
		return v1alpha1.ExecResult{}, fmt.Errorf("%w: stdin: the Machine API's exec takes no input", v1alpha1.ErrUnsupported)
	}
	r, err := p.api.Exec(ctx, string(name), machineapi.ExecRequest{Command: req.Command, TimeoutSeconds: seconds(req.Timeout)})
	if err != nil {
		return v1alpha1.ExecResult{}, translate("exec in the Machine "+string(name), err)
	}

	return v1alpha1.ExecResult{ExitCode: r.ExitCode, Stdout: r.Stdout, Stderr: r.Stderr}, nil
}

// ReadFile returns the file at path in the running Machine.
func (p *Provider) ReadFile(ctx context.Context, name v1alpha1.Name, path string) ([]byte, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return nil, err
	}
	if err := v1alpha1.CheckPath(path); err != nil {
		return nil, err
	}
	b, err := p.api.ReadFile(ctx, string(name), path)
	if err != nil {
		return nil, translate("read "+path+" in the Machine "+string(name), err)
	}

	return b, nil
}

// WriteFile replaces the file at path in the running Machine with content, whole.
func (p *Provider) WriteFile(ctx context.Context, name v1alpha1.Name, path string, content []byte) error {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return err
	}
	if err := v1alpha1.CheckPath(path); err != nil {
		return err
	}

	return translate("write "+path+" in the Machine "+string(name), p.api.WriteFile(ctx, string(name), path, content))
}

// Dial opens TLS to naru-proxy with the SNI of the port's service, which the proxy terminates and
// carries on to the Machine as plain TCP (DIAL-1). The proxy authenticates no caller, so the
// listener behind the port authenticates each connection itself.
func (p *Provider) Dial(ctx context.Context, name v1alpha1.Name, port uint16) (net.Conn, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return nil, err
	}
	p.mu.Lock()
	e, ok := p.record[name]
	var running, named bool
	var id string
	var address machineapi.Address
	if ok {
		running = phase(e.status.Phase) == v1alpha1.PhaseRunning
		named = slices.Contains(e.spec.Ports, port)
		id, address = e.id, e.status.Address
	}
	p.mu.Unlock()
	switch {
	case !ok:
		return nil, fmt.Errorf("%w: workspace %s", v1alpha1.ErrNotFound, name)
	case !running:
		return nil, fmt.Errorf("%w: workspace %s", v1alpha1.ErrNotRunning, name)
	case !named:
		return nil, fmt.Errorf("%w: port %d is not among the spec's ports", v1alpha1.ErrInvalid, port)
	}
	host, err := serviceHost(id, address, port)
	if err != nil {
		return nil, fmt.Errorf("dial port %d of the Machine %s: %v", port, name, err)
	}
	d := tls.Dialer{Config: &tls.Config{ServerName: host, RootCAs: p.cfg.ProxyRoots, MinVersion: tls.VersionTLS12}}
	conn, err := d.DialContext(ctx, "tcp", p.cfg.ProxyAddress)
	if err != nil {
		return nil, fmt.Errorf("dial port %d of the Machine %s through naru-proxy: %v", port, name, err)
	}

	return conn, nil
}
