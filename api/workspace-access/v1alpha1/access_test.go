package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

func TestCheckTokenPath_FILE1(t *testing.T) {
	for _, ok := range []string{"/run/garam-bot/agent/token", "/run/garam-bot/model-token", "/run/garam-bot/a"} {
		assert.NoError(t, v1alpha1.CheckTokenPath(ok), ok)
	}
	for _, bad := range []string{
		"/run/garam-bot/", "/run/garam-bot", "/run/garam-bot-x/token", "/run/garam-bot/../etc/passwd",
		"/run/garam-bot/a/", "/run/garam-bot//a", "/run/garam-bot/./a", "run/garam-bot/a", "/etc/passwd", "",
	} {
		assert.Error(t, v1alpha1.CheckTokenPath(bad), bad)
	}
}
