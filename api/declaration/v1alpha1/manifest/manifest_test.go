package manifest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	"github.com/garamsh/garam-bot/api/declaration/v1alpha1/manifest"
)

const mira = `apiVersion: bot.garam.sh/v1alpha1
kind: Bot
metadata: {name: mira}
spec:
  persona: {role: Game Lead}
  agent: {harness: claude-code}
`

func TestParse_ReturnsTheTypedObjectWithDefaults(t *testing.T) {
	object, err := manifest.Parse([]byte(mira))
	require.NoError(t, err)
	bot, ok := object.(*v1alpha1.Bot)
	require.True(t, ok, "Parse returned %T", object)
	assert.Equal(t, "mira", bot.Name)
	assert.Equal(t, "claude-code", bot.Spec.Agent.Harness)
	assert.Equal(t, v1alpha1.PlacementShared, bot.Spec.Placement)
}

func TestParse_FILE1_AdmitsOneJSONDocument(t *testing.T) {
	_, err := manifest.Parse([]byte(`{"apiVersion": "bot.garam.sh/v1alpha1", "kind": "BotPool",
		"metadata": {"name": "game-team"}, "spec": {"maxBotsPerPod": 50}}`))
	require.NoError(t, err)
}

func TestParse_FILE1_RefusesAFileThatIsNotOneDocument(t *testing.T) {
	// The control: the same object alone is admitted, also behind a document marker.
	_, err := manifest.Parse([]byte("---\n" + mira))
	require.NoError(t, err)

	for name, data := range map[string]string{
		"two documents":   mira + "---\n" + mira,
		"no document":     "# a comment\n",
		"empty":           "",
		"not an object":   "[1, 2]\n",
		"a duplicate key": mira + "metadata: {name: other}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := manifest.Parse([]byte(data))
			require.ErrorIs(t, err, manifest.ErrInvalid)
		})
	}
}

func TestParse_SCH3_RefusesAnUnknownMetadataField(t *testing.T) {
	_, err := manifest.Parse([]byte(mira + "  placement: Shared\n"))
	require.NoError(t, err)

	_, err = manifest.Parse([]byte(`apiVersion: bot.garam.sh/v1alpha1
kind: Bot
metadata: {name: mira, owner: sauce}
spec:
  persona: {role: Game Lead}
  agent: {harness: claude-code}
`))
	require.ErrorIs(t, err, manifest.ErrInvalid)
}
