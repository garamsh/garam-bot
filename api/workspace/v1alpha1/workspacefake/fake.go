// Package workspacefake is the in-memory implementation of the workspace provider interface,
// v1alpha1: the reference implementation the conformance suite runs against while the version is
// below v1. It runs no image. Its workspaces behave as the conformance image that
// ../conformance.md describes: Exec runs `true`, `false` and `cat`, and every port of a spec echoes
// what it is sent.
package workspacefake

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// Provider is an in-memory v1alpha1.Provider for one caller.
type Provider struct {
	caps v1alpha1.Capabilities

	mu         sync.Mutex
	workspaces map[v1alpha1.Name]*workspace
	snapshots  map[v1alpha1.Name]*snapshot
}

type workspace struct {
	spec  v1alpha1.Spec
	phase v1alpha1.Phase
	files map[string][]byte
}

type snapshot struct {
	of    v1alpha1.Name
	files map[string][]byte
}

var _ v1alpha1.Provider = (*Provider)(nil)

// New returns an empty Provider offering caps.
func New(caps v1alpha1.Capabilities) *Provider {
	return &Provider{
		caps:       caps,
		workspaces: map[v1alpha1.Name]*workspace{},
		snapshots:  map[v1alpha1.Name]*snapshot{},
	}
}

// Capabilities returns what New was given.
func (p *Provider) Capabilities(context.Context) (v1alpha1.Capabilities, error) {
	return p.caps, nil
}

// Create makes a running workspace at once.
func (p *Provider) Create(_ context.Context, name v1alpha1.Name, spec v1alpha1.Spec) (v1alpha1.Workspace, bool, error) {
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	if err := spec.Check(); err != nil {
		return v1alpha1.Workspace{}, false, err
	}
	if len(spec.Egress) > 0 && !p.caps.Egress {
		return v1alpha1.Workspace{}, false, fmt.Errorf("%w: egress rules", v1alpha1.ErrUnsupported)
	}
	if spec.FromSnapshot != "" && !p.caps.Snapshot {
		return v1alpha1.Workspace{}, false, fmt.Errorf("%w: restore", v1alpha1.ErrUnsupported)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if w, ok := p.workspaces[name]; ok {
		if !v1alpha1.SameSpec(w.spec, spec) {
			return v1alpha1.Workspace{}, false, fmt.Errorf("%w: %s holds another spec", v1alpha1.ErrConflict, name)
		}

		return w.answer(name), false, nil
	}
	files := map[string][]byte{}
	if spec.FromSnapshot != "" {
		s, ok := p.snapshots[spec.FromSnapshot]
		if !ok {
			return v1alpha1.Workspace{}, false, fmt.Errorf("%w: %s", v1alpha1.ErrSnapshotNotFound, spec.FromSnapshot)
		}
		files = maps.Clone(s.files)
	}
	w := &workspace{spec: clone(spec), phase: v1alpha1.PhaseRunning, files: files}
	p.workspaces[name] = w

	return w.answer(name), true, nil
}

// Get returns the workspace name.
func (p *Provider) Get(_ context.Context, name v1alpha1.Name) (v1alpha1.Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, err := p.workspace(name)
	if err != nil {
		return v1alpha1.Workspace{}, err
	}

	return w.answer(name), nil
}

// List returns every workspace, ordered by name.
func (p *Provider) List(context.Context) ([]v1alpha1.Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]v1alpha1.Workspace, 0, len(p.workspaces))
	for _, name := range slices.Sorted(maps.Keys(p.workspaces)) {
		out = append(out, p.workspaces[name].answer(name))
	}

	return out, nil
}

// Start runs a stopped workspace again.
func (p *Provider) Start(_ context.Context, name v1alpha1.Name) (v1alpha1.Workspace, error) {
	return p.setPhase(name, v1alpha1.PhaseRunning)
}

// Stop stops the workspace at once. What it held outside v1alpha1.DataPath is gone, as the
// weakest implementation the interface admits would lose it.
func (p *Provider) Stop(_ context.Context, name v1alpha1.Name, _ time.Duration) (v1alpha1.Workspace, error) {
	return p.setPhase(name, v1alpha1.PhaseStopped)
}

func (p *Provider) setPhase(name v1alpha1.Name, phase v1alpha1.Phase) (v1alpha1.Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, err := p.workspace(name)
	if err != nil {
		return v1alpha1.Workspace{}, err
	}
	if phase == v1alpha1.PhaseStopped {
		maps.DeleteFunc(w.files, func(file string, _ []byte) bool { return !inData(file) })
	}
	w.phase = phase

	return w.answer(name), nil
}

// Delete removes the workspace at once.
func (p *Provider) Delete(_ context.Context, name v1alpha1.Name) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.workspaces, name)

	return nil
}

// Exec runs the commands of the conformance image: true, false and cat. Any other exits 127.
func (p *Provider) Exec(_ context.Context, name v1alpha1.Name, req v1alpha1.ExecRequest) (v1alpha1.ExecResult, error) {
	if len(req.Command) == 0 {
		return v1alpha1.ExecResult{}, fmt.Errorf("%w: an empty command", v1alpha1.ErrInvalid)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.running(name); err != nil {
		return v1alpha1.ExecResult{}, err
	}
	switch req.Command[0] {
	case "true":
		return v1alpha1.ExecResult{Stdout: []byte{}, Stderr: []byte{}}, nil
	case "false":
		return v1alpha1.ExecResult{ExitCode: 1, Stdout: []byte{}, Stderr: []byte{}}, nil
	case "cat":
		return v1alpha1.ExecResult{Stdout: slices.Clone(req.Stdin), Stderr: []byte{}}, nil
	}

	return v1alpha1.ExecResult{ExitCode: 127, Stdout: []byte{}, Stderr: []byte(req.Command[0] + ": not found\n")}, nil
}

// ReadFile returns the file at file.
func (p *Provider) ReadFile(_ context.Context, name v1alpha1.Name, file string) ([]byte, error) {
	if err := checkPath(file); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	w, err := p.running(name)
	if err != nil {
		return nil, err
	}
	content, ok := w.files[file]
	if !ok {
		return nil, fmt.Errorf("%w: %s", v1alpha1.ErrFileNotFound, file)
	}

	return slices.Clone(content), nil
}

// WriteFile replaces the file at file.
func (p *Provider) WriteFile(_ context.Context, name v1alpha1.Name, file string, content []byte) error {
	if err := checkPath(file); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	w, err := p.running(name)
	if err != nil {
		return err
	}
	w.files[file] = slices.Clone(content)

	return nil
}

// Dial returns one end of a pipe whose other end echoes what it reads.
func (p *Provider) Dial(_ context.Context, name v1alpha1.Name, port uint16) (net.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w, err := p.running(name)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(w.spec.Ports, port) {
		return nil, fmt.Errorf("%w: port %d is not among the spec's ports", v1alpha1.ErrInvalid, port)
	}
	caller, workspace := net.Pipe()
	go func() {
		defer workspace.Close()
		_, _ = io.Copy(workspace, workspace)
	}()

	return caller, nil
}

// Snapshot copies the data volume of the workspace of, ready at once.
func (p *Provider) Snapshot(_ context.Context, name, of v1alpha1.Name) (v1alpha1.Snapshot, bool, error) {
	if !p.caps.Snapshot {
		return v1alpha1.Snapshot{}, false, fmt.Errorf("%w: snapshot", v1alpha1.ErrUnsupported)
	}
	if _, err := v1alpha1.ParseName(string(name)); err != nil {
		return v1alpha1.Snapshot{}, false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.snapshots[name]; ok {
		if s.of != of {
			return v1alpha1.Snapshot{}, false, fmt.Errorf("%w: %s is a snapshot of %s", v1alpha1.ErrConflict, name, s.of)
		}

		return s.answer(name), false, nil
	}
	w, err := p.workspace(of)
	if err != nil {
		return v1alpha1.Snapshot{}, false, err
	}
	s := &snapshot{of: of, files: maps.Clone(w.files)}
	maps.DeleteFunc(s.files, func(file string, _ []byte) bool { return !inData(file) })
	p.snapshots[name] = s

	return s.answer(name), true, nil
}

// GetSnapshot returns the snapshot name.
func (p *Provider) GetSnapshot(_ context.Context, name v1alpha1.Name) (v1alpha1.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.snapshots[name]
	if !ok {
		return v1alpha1.Snapshot{}, fmt.Errorf("%w: snapshot %s", v1alpha1.ErrNotFound, name)
	}

	return s.answer(name), nil
}

// DeleteSnapshot removes the snapshot name.
func (p *Provider) DeleteSnapshot(_ context.Context, name v1alpha1.Name) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.snapshots, name)

	return nil
}

func (p *Provider) workspace(name v1alpha1.Name) (*workspace, error) {
	w, ok := p.workspaces[name]
	if !ok {
		return nil, fmt.Errorf("%w: workspace %s", v1alpha1.ErrNotFound, name)
	}

	return w, nil
}

func (p *Provider) running(name v1alpha1.Name) (*workspace, error) {
	w, err := p.workspace(name)
	if err != nil {
		return nil, err
	}
	if w.phase != v1alpha1.PhaseRunning {
		return nil, fmt.Errorf("%w: workspace %s is %s", v1alpha1.ErrNotRunning, name, w.phase)
	}

	return w, nil
}

func (w *workspace) answer(name v1alpha1.Name) v1alpha1.Workspace {
	return v1alpha1.Workspace{Name: name, Spec: clone(w.spec), Status: v1alpha1.Status{Phase: w.phase}}
}

func (s *snapshot) answer(name v1alpha1.Name) v1alpha1.Snapshot {
	return v1alpha1.Snapshot{Name: name, Workspace: s.of, Phase: v1alpha1.SnapshotReady}
}

func clone(spec v1alpha1.Spec) v1alpha1.Spec {
	spec.Env = maps.Clone(spec.Env)
	spec.Ports = slices.Clone(spec.Ports)
	spec.Egress = slices.Clone(spec.Egress)

	return spec
}

func checkPath(file string) error {
	if !path.IsAbs(file) || path.Clean(file) != file {
		return fmt.Errorf("%w: path %q is not absolute and clean", v1alpha1.ErrInvalid, file)
	}

	return nil
}

func inData(file string) bool {
	return strings.HasPrefix(file, v1alpha1.DataPath+"/")
}
