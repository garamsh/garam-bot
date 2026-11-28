package naruworkspace

import (
	"context"

	"github.com/garamsh/naru/lib/machineapi"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// Snapshot puts the snapshot name of the Machine of's volume. naru answers a snapshot of another
// Machine under the name as a conflict (SNAP-1).
func (p *Provider) Snapshot(ctx context.Context, name, of v1alpha1.Name) (v1alpha1.Snapshot, bool, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Snapshot{}, false, err
	}
	if _, err := v1alpha1.ParseName(string(of)); err != nil {
		return v1alpha1.Snapshot{}, false, err
	}
	s, created, err := p.api.PutSnapshot(ctx, string(name), machineapi.SnapshotRequest{Machine: string(of)})
	if err != nil {
		return v1alpha1.Snapshot{}, false, translate("snapshot the Machine "+string(of), err)
	}

	return snapshot(s), created, nil
}

// GetSnapshot returns the snapshot name.
func (p *Provider) GetSnapshot(ctx context.Context, name v1alpha1.Name) (v1alpha1.Snapshot, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Snapshot{}, err
	}
	s, err := p.api.Snapshot(ctx, string(name))
	if err != nil {
		return v1alpha1.Snapshot{}, translate("read the snapshot "+string(name), err)
	}

	return snapshot(s), nil
}

// DeleteSnapshot deletes the snapshot name. A name the caller does not have is already deleted.
func (p *Provider) DeleteSnapshot(ctx context.Context, name v1alpha1.Name) error {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return err
	}
	err := p.api.DeleteSnapshot(ctx, string(name))
	if isCode(err, machineapi.CodeNotFound) {
		return nil
	}

	return translate("delete the snapshot "+string(name), err)
}

// snapshot is a Machine snapshot as interface 3 answers it: DiskBytes is the size of its one
// volume, which a restore's spec must equal (SNAP-2).
func snapshot(s machineapi.Snapshot) v1alpha1.Snapshot {
	return v1alpha1.Snapshot{
		Name:      v1alpha1.Name(s.Name),
		Workspace: v1alpha1.Name(s.Machine),
		DiskBytes: diskBytes(s.Spec),
		Phase:     snapshotPhase(s.Phase),
		Reason:    s.Reason,
	}
}
