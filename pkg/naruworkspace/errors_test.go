package naruworkspace

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/garamsh/naru/lib/machineapi"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

var sentinels = []error{
	v1alpha1.ErrInvalid, v1alpha1.ErrNotFound, v1alpha1.ErrSnapshotNotFound, v1alpha1.ErrConflict,
	v1alpha1.ErrDeleting, v1alpha1.ErrNotRunning, v1alpha1.ErrFileNotFound, v1alpha1.ErrUnsupported,
}

// TestTranslate_FollowsTheErrorTable holds README §Mapping onto naru's Machine API's error table, row
// by row: each code is its sentinel and matches no other, and the rest match none.
func TestTranslate_FollowsTheErrorTable(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   machineapi.Code
		want   error
	}{
		{422, machineapi.CodeInvalid, v1alpha1.ErrInvalid},
		{413, machineapi.CodeInvalid, v1alpha1.ErrInvalid},
		{422, machineapi.CodeSnapshotMismatch, v1alpha1.ErrInvalid},
		{404, machineapi.CodeNotFound, v1alpha1.ErrNotFound},
		{422, machineapi.CodeMachineNotFound, v1alpha1.ErrNotFound},
		{422, machineapi.CodeSnapshotNotFound, v1alpha1.ErrSnapshotNotFound},
		{409, machineapi.CodeConflict, v1alpha1.ErrConflict},
		{409, machineapi.CodeDeleting, v1alpha1.ErrDeleting},
		{409, machineapi.CodeNotRunning, v1alpha1.ErrNotRunning},
		{404, machineapi.CodeFileNotFound, v1alpha1.ErrFileNotFound},
		{409, "snapshot", v1alpha1.ErrUnsupported},
		{403, machineapi.CodeForbidden, nil},
		{500, machineapi.CodeInternal, nil},
		{503, machineapi.CodeUnavailable, nil},
		{410, machineapi.CodeCursorExpired, nil},
	} {
		err := translate("op", &machineapi.Error{Status: tc.status, Code: tc.code, Message: "m"})
		for _, s := range sentinels {
			assert.Equal(t, s == tc.want, errors.Is(err, s), "%d %s against %v", tc.status, tc.code, s)
		}
		var leaked *machineapi.Error
		assert.False(t, errors.As(err, &leaked), "the Machine API's error does not leave the package")
	}
	assert.NoError(t, translate("op", nil))
}

func mustPrefix(s string) netip.Prefix { return netip.MustParsePrefix(s) }
