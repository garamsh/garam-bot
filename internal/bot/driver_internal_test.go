package bot

import (
	"context"
	"crypto/rand"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/api/agent/v1alpha1/conformance"
	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

// The caller suite of interface 2 reaches the driver's client and its token files, which are not
// exported: the suite's adapter is scripted, and its tokens are issued here as the connector would.
// The driver reads no session's state, keeping its own of the tasks it gives, so ReadState is the
// client's read with VER-2's mapping here, and holds nothing of the driver's.

// TestAdapterClient_HoldsAnEventsReadPastMaxWaitWithinDIAL3 reads the client's bounds, which no
// adapter shows in a test's time: an events read held MaxWait is answered before the request times
// out, and the client opens at most two connections, below DIAL-3's four dials of an instance, each
// closed idle before DIAL-3's 120 seconds.
func TestAdapterClient_HoldsAnEventsReadPastMaxWaitWithinDIAL3(t *testing.T) {
	client := newAdapterClient(&redialer{}, func() (string, string) { return "", "" })
	assert.Greater(t, client.http.Timeout, agent.MaxWait*time.Second)
	transport, ok := client.http.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 2, transport.MaxConnsPerHost)
	assert.Less(t, transport.IdleConnTimeout, 120*time.Second)
}

// TestDriver_KeepsTheCallersRequirements runs conformance.md's caller suite against the driver.
func TestDriver_KeepsTheCallersRequirements(t *testing.T) {
	conformance.RunCaller(t, caller{})
}

type caller struct{}

// client is the driver's client of the adapter at c, dialled as the workspace would dial it.
func (caller) client(c conformance.Conn) (*adapterClient, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	dial := func(ctx context.Context) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", base.Host)
	}
	return newAdapterClient(&redialer{dial: dial, sleep: sleep}, func() (string, string) { return c.Token, "" }), nil
}

func (k caller) ReadEvents(ctx context.Context, c conformance.Conn, session string, after uint64) ([]agent.Event, uint64, error) {
	client, err := k.client(c)
	if err != nil {
		return nil, after, err
	}
	return client.events(ctx, session, after, 0)
}

func (k caller) ReadState(ctx context.Context, c conformance.Conn, session string) (agent.SessionState, error) {
	client, err := k.client(c)
	if err != nil {
		return "", err
	}
	var answered agent.Session
	if _, err := client.call(ctx, http.MethodGet, "sessions/"+session, nil, &answered); err != nil {
		return "", err
	}
	switch answered.State {
	case agent.SessionIdle, agent.SessionWorking:
		return answered.State, nil
	default:
		return agent.SessionUnknown, nil
	}
}

// Tokens writes what the workspace's one write of tokens holds, for the session tokens the connector
// issues the spec's model and tools, each living an hour as the reference's do, and Keep writes them
// again once they have stood as long as the workspace lets them.
func (caller) Tokens(ctx context.Context, files conformance.Files, spec agent.SessionSpec, place conformance.Place) (conformance.TokenKeeper, error) {
	k := &keeper{files: files, spec: spec, now: place.Now}
	return k, k.write(ctx)
}

type keeper struct {
	files conformance.Files
	spec  agent.SessionSpec
	now   func() time.Time
	due   time.Time
}

func (k *keeper) Keep(ctx context.Context) error {
	if k.now().Before(k.due) {
		return nil
	}
	return k.write(ctx)
}

func (k *keeper) write(ctx context.Context) error {
	tokens := &access.SessionTokens{
		Model: access.Token{Token: rand.Text(), ExpiresIn: 3600},
		Tools: map[string]access.Token{},
	}
	for _, tool := range k.spec.Tools {
		tokens.Tools[tool.Name] = access.Token{Token: rand.Text(), ExpiresIn: 3600}
	}
	_, files, stand, err := tokenFiles(tokens)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := k.files.WriteFile(ctx, file.Path, file.Content); err != nil {
			return err
		}
	}
	k.due = k.now().Add(stand)
	return nil
}
