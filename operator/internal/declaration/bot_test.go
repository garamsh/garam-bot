package declaration_test

import (
	"testing"

	botv1 "github.com/garamsh/garam-bot/api/declaration/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/garamsh/garam-bot/operator/internal/declaration"
)

const botMira = "bot-mira"

// bot is a Bot object in namespace, as a declaration carries it.
func bot(namespace, name string) map[string]any {
	return declaredObject(declaration.APIVersion, "Bot", namespace, name, map[string]any{
		"persona": map[string]any{"role": "Game Lead"}, "agent": map[string]any{"harness": "claude-code"},
	})
}

// miraRef is the Bot mira of the watched namespace.
var miraRef = declaration.Ref{Kind: "Bot", Namespace: watched, Name: "mira"}

// garamOwnedBot is a Bot another writer holds, marked as record's object for declaration id.
func garamOwnedBot(namespace, name, record, id string, generation int64) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: bot(namespace, name)}
	if err := botv1.MarkGaramOwned(o, botv1.GaramDeclaration{Cluster: record, ID: id, Generation: generation}); err != nil {
		panic(err)
	}
	return o
}

func TestBot_AnswersTheBotItAppliedWithTheUIDOfTheObjectItMade(t *testing.T) {
	h := newHarness(t)
	h.declare(botMira, bot(watched, "mira"))
	service := h.run(h.config())

	h.settled(service, hasOutcome(botMira, declaration.Applied))
	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "mira"); return ok }, eventually, tick)
	got, _ := service.Bot(watched, "mira")
	made, ok := h.objects.get(miraRef)
	require.True(t, ok)
	assert.Equal(t, declaration.AppliedBot{DeclarationID: botMira, UID: string(made.GetUID())}, got)
	_, ok = service.Bot(other, "mira")
	assert.False(t, ok, "a Bot of the same name in another namespace is not it")
}

func TestBot_RefusesAnObjectWhoseMarkerNamesADeclarationOfAnotherPlace(t *testing.T) {
	h := newHarness(t)
	h.declare(botMira, bot(other, "mira"))
	// Forged: a Bot in another namespace, marked as this record's object for mira's declaration.
	h.objects.put(garamOwnedBot(watched, "forged", h.record, botMira, 1))
	service := h.run(h.config())

	h.settled(service, hasOutcome(botMira, declaration.Applied))
	require.Eventually(t, func() bool { _, ok := service.Bot(other, "mira"); return ok }, eventually, tick,
		"control: the Bot at the place its declaration names is answered")
	_, ok := service.Bot(watched, "forged")
	assert.False(t, ok, "an object's marker does not make it a declaration's Bot")
}

func TestBot_RefusesTheBotADeclarationMovedAwayFrom(t *testing.T) {
	h := newHarness(t)
	h.declare(botMira, bot(watched, "mira"))
	service := h.run(h.config())
	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "mira"); return ok }, eventually, tick)

	h.declare(botMira, bot(other, "mira"))

	require.Eventually(t, func() bool { _, ok := service.Bot(other, "mira"); return ok }, eventually, tick,
		"control: the Bot at the place the latest generation names is answered")
	_, left := h.objects.get(miraRef)
	require.True(t, left, "the orphan stays in place (DECL-13)")
	_, ok := service.Bot(watched, "mira")
	assert.False(t, ok, "the orphan the move left is no declaration's Bot")
}

func TestBot_RefusesTheBotOfADeclarationTombstoned(t *testing.T) {
	h := newHarness(t)
	h.objects.refuse = func(op string, _ *unstructured.Unstructured) error {
		if op == "delete" {
			return assert.AnError
		}
		return nil
	}
	h.declare(botMira, bot(watched, "mira"))
	service := h.run(h.config())
	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "mira"); return ok }, eventually, tick)

	h.tombstone(botMira)

	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "mira"); return !ok }, eventually, tick,
		"a tombstoned declaration's Bot is refused while its object is still there")
	_, left := h.objects.get(miraRef)
	assert.True(t, left, "the object was not deleted, so the refusal is the tombstone's")
}

func TestBot_RefusesAnObjectMadeInThePlaceOfTheOneItMade(t *testing.T) {
	h := newHarness(t)
	generation := h.declare(botMira, bot(watched, "mira"))
	service := h.run(h.config())
	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "mira"); return ok }, eventually, tick)
	first, _ := service.Bot(watched, "mira")

	// Another writer deletes the object and makes one of the same name, marker and spec.
	h.objects.recreate(garamOwnedBot(watched, "mira", h.record, botMira, generation))
	replaced, _ := h.objects.get(miraRef)
	require.NotEqual(t, first.UID, string(replaced.GetUID()), "the object made in its place has another UID")

	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "mira"); return !ok }, eventually, tick,
		"the object made in the place of the connector's is not the Bot, though its marker and spec match")
}

func TestBot_RefusesABotOfANamespaceClaimedElsewhere(t *testing.T) {
	h := newHarness(t)
	h.objects.claim(other, "garam/another")
	h.declare(botMira, bot(other, "mira"))
	h.declare("bot-nova", bot(watched, "nova"))
	service := h.run(h.config())

	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "nova"); return ok }, eventually, tick,
		"control: the Bot of a claimed namespace is answered")
	_, ok := service.Bot(other, "mira")
	assert.False(t, ok)
}

func TestBot_RefusesABotItDidNotApply(t *testing.T) {
	h := newHarness(t)
	// Another writer's Bot holds the place the declaration names.
	h.objects.put(&unstructured.Unstructured{Object: bot(watched, "mira")})
	h.declare(botMira, bot(watched, "mira"))
	h.declare("bot-nova", bot(watched, "nova"))
	service := h.run(h.config())

	h.settled(service, hasOutcome(botMira, declaration.Conflict))
	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "nova"); return ok }, eventually, tick,
		"control: the Bot it applied beside it is answered")
	_, ok := service.Bot(watched, "mira")
	assert.False(t, ok, "a Bot at the declared place the connector did not apply is not the declaration's")
}

func TestBot_RefusesTheBotOfADeclarationTombstonedBeforeAPassFinishes(t *testing.T) {
	h := newHarness(t)
	h.declare(botMira, bot(watched, "mira"))
	service := h.run(h.config())
	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "mira"); return ok }, eventually, tick)

	h.objects.failLists()
	h.tombstone(botMira)

	require.Eventually(t, func() bool { _, ok := service.Bot(watched, "mira"); return !ok }, eventually, tick,
		"the tombstone ends the Bot's standing though no pass has run since")
}
