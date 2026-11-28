package naruworkspace

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/garamsh/naru/lib/machineapi"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// translate turns a Machine API refusal into interface 3's sentinel for it, by README §Mapping
// onto naru's Machine API's error table. Any other failure is returned matching no sentinel, and
// the Machine API's error does not leave this package.
func translate(op string, err error) error {
	if err == nil {
		return nil
	}
	var refusal *machineapi.Error
	if !errors.As(err, &refusal) {
		return fmt.Errorf("%s: %v", op, err)
	}
	sentinel := sentinelOf(refusal)
	if sentinel == nil {
		return fmt.Errorf("%s: %v", op, refusal)
	}

	return fmt.Errorf("%w: %s: %s", sentinel, op, refusal.Message)
}

func sentinelOf(e *machineapi.Error) error {
	switch e.Code {
	case machineapi.CodeInvalid, machineapi.CodeSnapshotMismatch:
		return v1alpha1.ErrInvalid
	case machineapi.CodeNotFound, machineapi.CodeMachineNotFound:
		return v1alpha1.ErrNotFound
	case machineapi.CodeSnapshotNotFound:
		return v1alpha1.ErrSnapshotNotFound
	case machineapi.CodeConflict:
		return v1alpha1.ErrConflict
	case machineapi.CodeDeleting:
		return v1alpha1.ErrDeleting
	case machineapi.CodeNotRunning:
		return v1alpha1.ErrNotRunning
	case machineapi.CodeFileNotFound:
		return v1alpha1.ErrFileNotFound
	}
	// A call a Machine's capabilities exclude is a 409 naming the capability (naru ADR 0108 item 8).
	if e.Status == http.StatusConflict {
		return v1alpha1.ErrUnsupported
	}

	return nil
}

// isCode reports whether err is a Machine API refusal of code.
func isCode(err error, code machineapi.Code) bool {
	var refusal *machineapi.Error
	return errors.As(err, &refusal) && refusal.Code == code
}
