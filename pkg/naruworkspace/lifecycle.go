package naruworkspace

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/garamsh/naru/lib/machineapi"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// Create puts the Machine name with spec's Machine spec, running. naru compares the spec it was
// given, which is SameSpec's comparison, so the provider compares nothing itself (LIFE-1).
func (p *Provider) Create(ctx context.Context, name v1alpha1.Name, spec v1alpha1.Spec) (v1alpha1.Workspace, bool, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	if err := spec.Check(); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	m, created, err := p.api.CreateMachine(ctx, string(name), machineSpec(spec, p.cfg.RootDiskBytes))
	if err != nil {
		return v1alpha1.Workspace{}, false, translate("create the Machine "+string(name), err)
	}

	return p.learn(m), created, nil
}

// Get answers the workspace from the provider's record, and calls nothing.
func (p *Provider) Get(_ context.Context, name v1alpha1.Name) (v1alpha1.Workspace, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Workspace{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.record[name]
	if !ok {
		return v1alpha1.Workspace{}, fmt.Errorf("%w: workspace %s", v1alpha1.ErrNotFound, name)
	}

	return workspace(name, e), nil
}

// List answers every workspace of the record, ordered by name, and calls nothing.
func (p *Provider) List(context.Context) ([]v1alpha1.Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]v1alpha1.Workspace, 0, len(p.record))
	for _, name := range slices.Sorted(maps.Keys(p.record)) {
		out = append(out, workspace(name, p.record[name]))
	}

	return out, nil
}

// Start sets the Machine's desired state to running.
func (p *Provider) Start(ctx context.Context, name v1alpha1.Name) (v1alpha1.Workspace, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Workspace{}, err
	}
	m, err := p.api.StartMachine(ctx, string(name))
	if err != nil {
		return v1alpha1.Workspace{}, translate("start the Machine "+string(name), err)
	}

	return p.learn(m), nil
}

// Stop sets the Machine's desired state to stopped, grace rounded up to whole seconds. A grace
// above naru's bound is naru's to refuse.
func (p *Provider) Stop(ctx context.Context, name v1alpha1.Name, grace time.Duration) (v1alpha1.Workspace, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Workspace{}, err
	}
	m, err := p.api.StopMachine(ctx, string(name), machineapi.StopRequest{GraceSeconds: seconds(grace)})
	if err != nil {
		return v1alpha1.Workspace{}, translate("stop the Machine "+string(name), err)
	}

	return p.learn(m), nil
}

// Delete asks for the Machine and its volume to go; its snapshots stay. A name the caller does not
// have is already deleted. The record keeps the Machine until the event log reports it gone.
func (p *Provider) Delete(ctx context.Context, name v1alpha1.Name) error {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return err
	}
	err := p.api.DeleteMachine(ctx, string(name))
	if isCode(err, machineapi.CodeNotFound) {
		return nil
	}

	return translate("delete the Machine "+string(name), err)
}

// seconds is d in whole seconds, rounded up, and 0 for a d that is not above zero.
func seconds(d time.Duration) uint32 {
	if d <= 0 {
		return 0
	}
	s := (d + time.Second - 1) / time.Second
	if s > time.Duration(^uint32(0)) {
		return ^uint32(0)
	}

	return uint32(s)
}
