package podworkspace

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

const (
	snapshotGroup        = "snapshot.storage.k8s.io"
	snapshotGroupVersion = snapshotGroup + "/v1"

	// annotationDiskBytes holds the size of the data volume a snapshot was taken of (SNAP-2).
	annotationDiskBytes = "garam-bot.garam.sh/disk-bytes"
)

var snapshotResource = schema.GroupVersionResource{Group: snapshotGroup, Version: "v1", Resource: "volumesnapshots"}

// Snapshot takes the data volume of workspace of into the VolumeSnapshot name (SNAP-1).
func (p *Provider) Snapshot(ctx context.Context, name v1alpha1.Name, of v1alpha1.Name) (v1alpha1.Snapshot, bool, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Snapshot{}, false, err
	}
	if err := p.requireSnapshots(); err != nil {
		return v1alpha1.Snapshot{}, false, err
	}
	_, spec, err := p.record(ctx, of)
	if err != nil {
		return v1alpha1.Snapshot{}, false, err
	}
	client := p.dynamic.Resource(snapshotResource).Namespace(p.cfg.Namespace)
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": snapshotGroupVersion,
		"kind":       "VolumeSnapshot",
		"metadata": map[string]any{
			"name":        string(name),
			"labels":      map[string]any{labelWorkspace: string(of)},
			"annotations": map[string]any{annotationDiskBytes: strconv.FormatUint(spec.Resources.DiskBytes, 10)},
		},
		"spec": map[string]any{"source": map[string]any{"persistentVolumeClaimName": string(of)}},
	}}
	if p.cfg.SnapshotClass != "" {
		_ = unstructured.SetNestedField(object.Object, p.cfg.SnapshotClass, "spec", "volumeSnapshotClassName")
	}
	created, err := client.Create(ctx, object, metav1.CreateOptions{})
	if err == nil {
		return snapshotOf(created), true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return v1alpha1.Snapshot{}, false, fmt.Errorf("create snapshot %s: %w", name, err)
	}
	existing, err := p.GetSnapshot(ctx, name)
	if err != nil {
		return v1alpha1.Snapshot{}, false, err
	}
	if existing.Workspace != of {
		return v1alpha1.Snapshot{}, false, fmt.Errorf("%w: snapshot %s is of %s", v1alpha1.ErrConflict, name, existing.Workspace)
	}

	return existing, false, nil
}

// GetSnapshot returns the snapshot name.
func (p *Provider) GetSnapshot(ctx context.Context, name v1alpha1.Name) (v1alpha1.Snapshot, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Snapshot{}, err
	}
	served, err := p.snapshotsServed()
	if err != nil {
		return v1alpha1.Snapshot{}, err
	}
	if !served {
		return v1alpha1.Snapshot{}, fmt.Errorf("%w: snapshot %s", v1alpha1.ErrNotFound, name)
	}
	object, err := p.dynamic.Resource(snapshotResource).Namespace(p.cfg.Namespace).Get(ctx, string(name), metav1.GetOptions{})
	if err != nil {
		if isNotFound(err) {
			return v1alpha1.Snapshot{}, fmt.Errorf("%w: snapshot %s", v1alpha1.ErrNotFound, name)
		}

		return v1alpha1.Snapshot{}, fmt.Errorf("get snapshot %s: %w", name, err)
	}
	if object.GetLabels()[labelWorkspace] == "" {
		return v1alpha1.Snapshot{}, fmt.Errorf("%w: snapshot %s", v1alpha1.ErrNotFound, name)
	}

	return snapshotOf(object), nil
}

// DeleteSnapshot deletes the snapshot name (SNAP-1).
func (p *Provider) DeleteSnapshot(ctx context.Context, name v1alpha1.Name) error {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return err
	}
	if _, err := p.GetSnapshot(ctx, name); err != nil {
		if errors.Is(err, v1alpha1.ErrNotFound) {
			return nil
		}

		return err
	}
	err := p.dynamic.Resource(snapshotResource).Namespace(p.cfg.Namespace).Delete(ctx, string(name), metav1.DeleteOptions{})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("delete snapshot %s: %w", name, err)
	}

	return nil
}

// checkRestore refuses a restore from a snapshot the caller does not have, or of another size
// (SNAP-2).
func (p *Provider) checkRestore(ctx context.Context, spec v1alpha1.Spec) error {
	snap, err := p.GetSnapshot(ctx, spec.FromSnapshot)
	if err != nil {
		if errors.Is(err, v1alpha1.ErrNotFound) {
			return fmt.Errorf("%w: %s", v1alpha1.ErrSnapshotNotFound, spec.FromSnapshot)
		}

		return err
	}
	if snap.DiskBytes != spec.Resources.DiskBytes {
		return fmt.Errorf("%w: disk is %d bytes, and snapshot %s holds %d", v1alpha1.ErrInvalid, spec.Resources.DiskBytes, snap.Name, snap.DiskBytes)
	}

	return nil
}

func (p *Provider) requireSnapshots() error {
	served, err := p.snapshotsServed()
	if err != nil {
		return err
	}
	if !served {
		return fmt.Errorf("%w: this cluster serves no snapshots", v1alpha1.ErrUnsupported)
	}

	return nil
}

func snapshotOf(object *unstructured.Unstructured) v1alpha1.Snapshot {
	disk, _ := strconv.ParseUint(object.GetAnnotations()[annotationDiskBytes], 10, 64)
	snap := v1alpha1.Snapshot{
		Name:      v1alpha1.Name(object.GetName()),
		Workspace: v1alpha1.Name(object.GetLabels()[labelWorkspace]),
		DiskBytes: disk,
		Phase:     v1alpha1.SnapshotPending,
	}
	if message, found, _ := unstructured.NestedString(object.Object, "status", "error", "message"); found && message != "" {
		snap.Phase, snap.Reason = v1alpha1.SnapshotFailed, message
	} else if ready, found, _ := unstructured.NestedBool(object.Object, "status", "readyToUse"); found && ready {
		snap.Phase = v1alpha1.SnapshotReady
	}

	return snap
}
