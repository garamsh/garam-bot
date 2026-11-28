package workspaceaccess

import (
	"context"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A dropped pin changes no answer, since a restart or a new Pod is a new instance anyway, so the
// pins are read here, inside the package (conformance.md §PIN-1).

const (
	team = "game-team"
	// testOrigin is the origin the tests' services are reached at.
	testOrigin      = "https://garam.invalid"
	livingContainer = "c-living"
	livingPod       = "living"
	livingUID       = "pod-living"
)

type pods map[string]Pod

func (p pods) Pod(_ context.Context, _, name string) (Pod, bool, error) {
	pod, ok := p[name]
	return pod, ok, nil
}

type noKeys struct{}

func (noKeys) Keys(context.Context) (Issuer, error) { return Issuer{Keys: jose.JSONWebKeySet{}}, nil }

type noBots struct{}

func (noBots) Served(context.Context, string) (bool, error)     { return true, nil }
func (noBots) Bot(context.Context, Bot) (Declared, bool, error) { return Declared{}, false, nil }
func (noBots) Class(context.Context, string, string) (Class, bool, error) {
	return Class{}, false, nil
}

func TestRecheckPins_DropsThePinOfAnInstanceThatEndedAndKeepsALivingOnes(t *testing.T) {
	held := pods{
		livingPod:   {UID: livingUID, Container: livingContainer},
		"restarted": {UID: "pod-restarted", Container: "c-new"},
		"remade":    {UID: "pod-remade-new", Container: "c-remade"},
	}
	built, err := NewService(Config{
		Release: Release{Namespace: "garam-system", Name: "garam"}, Origins: []string{testOrigin},
		Keys: noKeys{}, Pods: held, Bots: noBots{},
	})
	require.NoError(t, err)
	s, ok := built.(*service)
	require.True(t, ok)
	living := instance{namespace: team, name: livingPod, pod: livingUID, container: livingContainer}
	pinned := []instance{
		living,
		{namespace: team, name: "ended", pod: "pod-ended", container: "c-ended"},
		{namespace: team, name: "restarted", pod: "pod-restarted", container: "c-old"},
		{namespace: team, name: "remade", pod: "pod-remade-old", container: "c-remade"},
	}
	for _, i := range pinned {
		require.True(t, s.pins.take(i, "key-"+i.name))
	}

	s.recheckPins(context.Background())

	assert.Equal(t, []instance{living}, s.pins.instances())
}

func TestRun_DropsAGonePodsPin(t *testing.T) {
	held := pods{livingPod: {UID: livingUID, Container: livingContainer}}
	built, err := NewService(Config{
		Release: Release{Namespace: "garam-system", Name: "garam"}, Origins: []string{testOrigin},
		Keys: noKeys{}, Pods: held, Bots: noBots{}, Recheck: 10 * time.Millisecond, BindingFresh: time.Nanosecond,
	})
	require.NoError(t, err)
	s, ok := built.(*service)
	require.True(t, ok)
	living := instance{namespace: team, name: livingPod, pod: livingUID, container: livingContainer}
	require.True(t, s.pins.take(living, "key-living"))
	require.True(t, s.pins.take(instance{namespace: team, name: "ended", pod: "pod-ended", container: "c-ended"}, "key-ended"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	assert.Eventually(t, func() bool {
		return assert.ObjectsAreEqual([]instance{living}, s.pins.instances())
	}, 2*time.Second, 10*time.Millisecond)
}
