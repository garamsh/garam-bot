package workspaceaccess

import (
	"errors"
	"fmt"
	"net/http"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// maxFilesBody bounds a write's body: 64 KiB of content in base64, and its paths and members.
const maxFilesBody = 4 * access.MaxContentBytes

// files writes the body's files, in order, under the token directory, through interface 3's WriteFile,
// which follows no link (FILE-1, interface 3's FILE-3).
func (s *service) files(w http.ResponseWriter, r *http.Request, c caller) error {
	var body access.Files
	err := decodeWhole(http.MaxBytesReader(w, r.Body, maxFilesBody), &body)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return errTooLarge
	case err != nil:
		return fmt.Errorf("%w: the body is not Files", errInvalid)
	case len(body.Files) == 0 || len(body.Files) > access.MaxFiles:
		return fmt.Errorf("%w: a write carries 1 to %d files", errInvalid, access.MaxFiles)
	}
	size := 0
	for _, f := range body.Files {
		if err := access.CheckTokenPath(f.Path); err != nil {
			return fmt.Errorf("%w: %w", errInvalid, err)
		}
		size += len(f.Content)
	}
	if size > access.MaxContentBytes {
		return errTooLarge
	}
	ctx := r.Context()
	t, err := s.targetOf(ctx, c)
	if err != nil {
		return err
	}
	if err := s.botLimits.take(c.declared.UID); err != nil {
		return err
	}
	ws, err := t.place.Provider.Get(ctx, t.name)
	if err != nil {
		return providerError(err)
	}
	if ws.Status.Phase != workspace.PhaseRunning {
		return fmt.Errorf("%w: it is %s", errNotRunning, ws.Status.Phase)
	}
	for _, f := range body.Files {
		err := t.place.Provider.WriteFile(ctx, t.name, f.Path, f.Content)
		switch {
		case err == nil:
			continue
		case errors.Is(err, workspace.ErrInvalid):
			// The path passed CheckTokenPath, so the provider refused a link on it.
			return fileError{path: f.Path, err: fmt.Errorf("%w: the path passes through a link", errInvalid)}
		case errors.Is(err, workspace.ErrNotRunning):
			return fileError{path: f.Path, err: fmt.Errorf("%w: %w", errNotRunning, err)}
		}
		return fileError{path: f.Path, err: err}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
