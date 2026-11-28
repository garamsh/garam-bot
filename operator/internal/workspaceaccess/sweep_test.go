package workspaceaccess

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/api/workspace/v1alpha1/workspacefake"
)

// The sweep is read inside the package: a pass is run at a moved clock, which no call of interface 7
// reaches (ADR 0099 decision 6).

const (
	releaseNamespace = "garam-system"
	miraName         = "mira"
)

var (
	sweepRelease = Release{Namespace: releaseNamespace, Name: "garam"}
	mira         = Bot{Namespace: team, Name: miraName}
)

// apiServer answers the live Bots, and the namespaces' claims.
type apiServer struct {
	mu        sync.Mutex
	live      map[Bot]string
	failing   bool
	unclaimed map[string]bool
}

func (a *apiServer) UID(_ context.Context, b Bot) (string, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failing {
		return "", false, errors.New("the API server does not answer")
	}
	uid, ok := a.live[b]
	return uid, ok, nil
}

func (a *apiServer) Served(_ context.Context, namespace string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.unclaimed[namespace], nil
}

func (a *apiServer) Bot(context.Context, Bot) (Declared, bool, error) { return Declared{}, false, nil }
func (a *apiServer) Class(context.Context, string, string) (Class, bool, error) {
	return Class{}, false, nil
}

func (a *apiServer) set(change func(*apiServer)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	change(a)
}

type sweepHarness struct {
	t        *testing.T
	service  *service
	api      *apiServer
	provider *workspacefake.Provider
	now      time.Time
}

func newSweepHarness(t *testing.T, caps workspace.Capabilities) *sweepHarness {
	t.Helper()
	h := &sweepHarness{
		t: t, api: &apiServer{live: map[Bot]string{}, unclaimed: map[string]bool{}},
		provider: workspacefake.New(caps), now: time.Unix(1_800_000_000, 0),
	}
	built, err := NewService(Config{
		Release: sweepRelease, Namespaces: []string{team, "other-team"}, Origins: []string{"https://garam.invalid"},
		Keys: noKeys{}, Pods: pods{}, Bots: h.api, LiveBots: h.api,
		Places: map[string]Place{"pod": {Provider: h.provider}},
		Now:    func() time.Time { return h.now },
	})
	require.NoError(t, err)
	s, ok := built.(*service)
	require.True(t, ok)
	h.service = s
	return h
}

// workspace creates the workspace the connector gives the Bot's attachment main, its labels changed by
// change, and answers its name.
func (h *sweepHarness) workspace(bot Bot, uid string, change func(name *workspace.Name, labels map[string]string)) workspace.Name {
	h.t.Helper()
	name := workspaceName(sweepRelease, bot, uid, "main")
	labels := labelsOf(sweepRelease, bot, uid, "main")
	if change != nil {
		change(&name, labels)
	}
	spec := workspace.Spec{
		Image:     "registry.invalid/agent@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Resources: workspace.Resources{CPU: 1, MemoryBytes: 1 << 30, DiskBytes: 1 << 30}, Ports: []uint16{7700},
	}
	if labels != nil {
		spec.Labels = labels
	}
	_, _, err := h.provider.Create(context.Background(), name, spec)
	require.NoError(h.t, err)
	return name
}

// passAt runs a sweep pass at after past the harness's start.
func (h *sweepHarness) passAt(after time.Duration) { h.service.sweep(context.Background(), after) }

func (h *sweepHarness) exists(name workspace.Name) bool {
	h.t.Helper()
	_, err := h.provider.Get(context.Background(), name)
	if errors.Is(err, workspace.ErrNotFound) {
		return false
	}
	require.NoError(h.t, err)
	return true
}

func TestSweep_DeletesAGoneBotsWorkspaceOnceTheGraceHasPassedAndKeepsALivingOnes(t *testing.T) {
	tests := []struct {
		name string
		live func(map[Bot]string)
	}{
		{"a Bot that is gone", func(map[Bot]string) {}},
		{"a Bot made again under its name", func(live map[Bot]string) { live[mira] = "remade-uid" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSweepHarness(t, workspace.Capabilities{Labels: true})
			rin := Bot{Namespace: team, Name: "rin"}
			h.api.set(func(a *apiServer) { a.live[rin] = "rin-uid" })
			gone, living := h.workspace(mira, "mira-uid", nil), h.workspace(rin, "rin-uid", nil)
			h.api.set(func(a *apiServer) { tt.live(a.live) })

			h.passAt(0)
			h.passAt(sweepGrace - time.Second)
			require.True(t, h.exists(gone), "the grace has not passed")
			h.passAt(sweepGrace)

			assert.False(t, h.exists(gone), "a gone Bot's workspace is deleted")
			assert.True(t, h.exists(living), "a living Bot's workspace is kept")
		})
	}
}

func TestSweep_TouchesNoWorkspaceWithoutEvidenceItIsThisReleasesAndItsBotGone(t *testing.T) {
	unlabelled := func(_ *workspace.Name, l map[string]string) {
		for key := range l {
			delete(l, key)
		}
	}
	tests := []struct {
		name string
		// bot owns the workspace, and is mira where it is not set.
		bot    Bot
		change func(name *workspace.Name, labels map[string]string)
		api    func(*apiServer)
		// controlKept is set where the refusal is the place's, so the control is kept too.
		controlKept bool
	}{
		{name: "another release's labels", change: func(_ *workspace.Name, l map[string]string) {
			l[LabelReleaseName] = "other"
		}},
		{name: "another release's prefix", change: func(n *workspace.Name, _ map[string]string) {
			*n = workspace.Name(Release{Namespace: releaseNamespace, Name: "other"}.Prefix() + string(*n)[14:])
		}},
		{name: "a name its labels do not give", change: func(_ *workspace.Name, l map[string]string) {
			l[LabelAttachment] = "scratch"
		}},
		{name: "no labels", change: unlabelled},
		{name: "a namespace no longer watched", bot: Bot{Namespace: "former-team", Name: miraName}},
		{name: "a namespace no longer claimed", api: func(a *apiServer) { a.unclaimed[team] = true }},
		{name: "an API server that does not answer", api: func(a *apiServer) { a.failing = true }, controlKept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSweepHarness(t, workspace.Capabilities{Labels: true})
			owner := mira
			if tt.bot != (Bot{}) {
				owner = tt.bot
			}
			kept := h.workspace(owner, "mira-uid", tt.change)
			// Control: a workspace of a gone Bot of the same name, in a namespace still watched and claimed.
			control := h.workspace(Bot{Namespace: "other-team", Name: miraName}, "mira-uid", nil)
			if tt.api != nil {
				h.api.set(tt.api)
			}

			h.passAt(0)
			h.passAt(sweepGrace)

			assert.True(t, h.exists(kept))
			assert.Equal(t, tt.controlKept, h.exists(control), "the control")
		})
	}
}

// labelsUnkept is a provider that answers labels but does not report keeping them.
type labelsUnkept struct {
	*workspacefake.Provider
}

func (labelsUnkept) Capabilities(context.Context) (workspace.Capabilities, error) {
	return workspace.Capabilities{}, nil
}

func TestSweep_ListsNoProviderThatDoesNotKeepLabels(t *testing.T) {
	h := newSweepHarness(t, workspace.Capabilities{Labels: true})
	name := h.workspace(mira, "mira-uid", nil)
	h.service.cfg.Places["pod"] = Place{Provider: labelsUnkept{h.provider}}

	h.passAt(0)
	h.passAt(sweepGrace)

	assert.True(t, h.exists(name))
	// Control: the same provider, reporting its labels, is swept.
	h.service.cfg.Places["pod"] = Place{Provider: h.provider}
	h.passAt(2 * sweepGrace)
	h.passAt(3 * sweepGrace)
	assert.False(t, h.exists(name))
}

func TestSweep_StartsTheGraceAgainWhenTheBotIsFoundLiving(t *testing.T) {
	h := newSweepHarness(t, workspace.Capabilities{Labels: true})
	name := h.workspace(mira, "mira-uid", nil)

	h.passAt(0) // found gone
	h.api.set(func(a *apiServer) { a.live[mira] = "mira-uid" })
	h.passAt(sweepGrace / 2) // found living: the finding is forgotten
	h.api.set(func(a *apiServer) { delete(a.live, mira) })
	h.passAt(sweepGrace/2 + time.Second) // found gone again
	h.passAt(sweepGrace)

	assert.True(t, h.exists(name), "the grace counts from the latest finding")
	h.passAt(sweepGrace + sweepGrace/2 + time.Second)
	assert.False(t, h.exists(name))
}

func TestSweep_KeepsAFindingThroughAnAPIServerThatDoesNotAnswer(t *testing.T) {
	h := newSweepHarness(t, workspace.Capabilities{Labels: true})
	name := h.workspace(mira, "mira-uid", nil)

	h.passAt(0) // found gone
	h.api.set(func(a *apiServer) { a.failing = true })
	h.passAt(sweepGrace / 2)
	h.api.set(func(a *apiServer) { a.failing = false })
	h.passAt(sweepGrace)

	assert.False(t, h.exists(name), "the grace counts from the first finding")
}
