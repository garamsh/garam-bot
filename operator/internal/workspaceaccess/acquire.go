package workspaceaccess

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// target is the workspace a caller's attachment names, and where it is made.
type target struct {
	name     workspace.Name
	provider string
	place    Place
	class    Class
}

// targetOf resolves the caller's attachment to its workspace: its class, the provider the class names,
// and the name the Bot's identity gives it, or errUnsupported where the server cannot serve it (WS-1).
func (s *service) targetOf(ctx context.Context, c caller) (target, error) {
	attachment := c.declared.Attachments[c.attachment]
	if attachment.Unsupported != "" {
		return target{}, fmt.Errorf("%w: %s", errUnsupported, attachment.Unsupported)
	}
	class, ok, err := s.cfg.Bots.Class(ctx, c.bot.Namespace, attachment.Class)
	switch {
	case err != nil:
		return target{}, err
	case !ok:
		return target{}, fmt.Errorf("%w: the namespace declares no WorkspaceClass %q", errUnsupported, attachment.Class)
	}
	place, ok := s.cfg.Places[class.Provider]
	if !ok {
		return target{}, fmt.Errorf("%w: the connector runs no %q provider", errUnsupported, class.Provider)
	}
	return target{
		name:     workspaceName(s.cfg.Release, c.bot, c.declared.UID, c.attachment),
		provider: class.Provider, place: place, class: class,
	}, nil
}

// acquire creates, starts or recovers the workspace, and answers its state (WS-1).
func (s *service) acquire(w http.ResponseWriter, r *http.Request, c caller) error {
	if err := noBody(r); err != nil {
		return err
	}
	ctx := r.Context()
	t, err := s.targetOf(ctx, c)
	if err != nil {
		return err
	}
	image, ok := s.cfg.Images[c.declared.Harness]
	if !ok {
		return fmt.Errorf("%w: the connector has no image for the harness %q", errUnsupported, c.declared.Harness)
	}
	caps, err := t.place.Provider.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("read the %s provider's capabilities: %w", t.provider, err)
	}
	spec := specOf(image, t.class, t.place)
	if caps.Labels {
		spec.Labels = labelsOf(s.cfg.Release, c.bot, c.declared.UID, c.attachment)
	}
	if err := s.botLimits.take(c.declared.UID); err != nil {
		return err
	}
	ws, _, err := t.place.Provider.Create(ctx, t.name, spec)
	if err != nil {
		return providerError(err)
	}
	switch ws.Status.Phase {
	case workspace.PhaseStopped:
		ws, err = t.place.Provider.Start(ctx, t.name)
	case workspace.PhaseFailed:
		// Its workload is made again and its data volume kept, as a stop and a start do.
		if _, err = t.place.Provider.Stop(ctx, t.name, 0); err == nil {
			ws, err = t.place.Provider.Start(ctx, t.name)
		}
	}
	if err != nil {
		return providerError(err)
	}
	s.writeWorkspace(w, c, t, ws)
	return nil
}

// read answers the workspace's state and takes nothing from the Bot's limit (WS-2).
func (s *service) read(w http.ResponseWriter, r *http.Request, c caller) error {
	ctx := r.Context()
	t, err := s.targetOf(ctx, c)
	if err != nil {
		return err
	}
	ws, err := t.place.Provider.Get(ctx, t.name)
	if err != nil {
		return providerError(err)
	}
	s.writeWorkspace(w, c, t, ws)
	return nil
}

// writeWorkspace answers ws as interface 7's Workspace, with Retry-After while it is pending (WS-3).
func (s *service) writeWorkspace(w http.ResponseWriter, c caller, t target, ws workspace.Workspace) {
	tools := make([]access.Tool, 0, len(c.declared.Tools))
	for _, name := range slices.Sorted(slices.Values(c.declared.Tools)) {
		tools = append(tools, access.Tool{Name: name, URL: strings.TrimRight(t.place.ToolProxyURL, "/") + "/" + name})
	}
	if ws.Status.Phase == workspace.PhasePending {
		retry(w, 0)
	}
	writeJSON(w, http.StatusOK, access.Workspace{
		Attachment: c.attachment, Phase: ws.Status.Phase, Reason: ws.Status.Reason,
		Model: access.Model{BaseURL: t.place.ModelURL}, Tools: tools,
	})
}

// providerError is interface 3's error as interface 7 answers it (README §Answers and refusals).
func providerError(err error) error {
	switch {
	case errors.Is(err, workspace.ErrNotFound):
		return fmt.Errorf("%w: the workspace is not acquired", errNotFound)
	case errors.Is(err, workspace.ErrDeleting):
		return fmt.Errorf("%w: %w", errDeleting, err)
	case errors.Is(err, workspace.ErrNotRunning):
		return fmt.Errorf("%w: %w", errNotRunning, err)
	case errors.Is(err, workspace.ErrInvalid), errors.Is(err, workspace.ErrUnsupported):
		return fmt.Errorf("%w: %w", errUnsupported, err)
	}
	// ErrConflict among them: the server's names are its own, so a conflict is the server's to cure.
	return err
}
