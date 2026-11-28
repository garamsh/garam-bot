package workspaceaccess_test

import (
	"bufio"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

// dial opens a dial of main, and answers its status and, where it opened, its connection.
func (p *process) dial(t *testing.T) (int, net.Conn) {
	t.Helper()
	return p.dialWith(t, "")
}

// dialWith opens a dial whose request carries body.
func (p *process) dialWith(t *testing.T, body string) (int, net.Conn) {
	t.Helper()
	path := mainPath + access.DialSuffix
	u, err := url.Parse(p.h.server.URL)
	require.NoError(t, err)
	transport, ok := p.h.server.Client().Transport.(*http.Transport)
	require.True(t, ok)
	conn, err := tls.Dial("tcp", u.Host, transport.TLSClientConfig)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodGet, p.h.server.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+p.credential)
	request.Header.Set(access.HeaderProof, p.proof(http.MethodGet, path))
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", access.DialProtocol)
	require.NoError(t, request.Write(conn))
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	require.NoError(t, err)
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return response.StatusCode, nil
	}
	t.Cleanup(func() { _ = conn.Close() })
	return response.StatusCode, conn
}

// dialAtOnce opens each times from each of processes at once, and answers how many each opened and
// the statuses of the rest.
func dialAtOnce(t *testing.T, processes []*process, each int) (map[*process]int, map[int]int) {
	t.Helper()
	var mu sync.Mutex
	opened := map[*process]int{}
	refused := map[int]int{}
	var wg sync.WaitGroup
	for _, p := range processes {
		for range each {
			wg.Go(func() {
				status, _ := p.dial(t)
				mu.Lock()
				defer mu.Unlock()
				if status == http.StatusSwitchingProtocols {
					opened[p]++
				} else {
					refused[status]++
				}
			})
		}
	}
	wg.Wait()
	return opened, refused
}

func TestDial_HoldsAnInstancesBoundUnderDialsAtOnce(t *testing.T) {
	h := newHarness(t, options{})
	p := h.instance(h.bot())
	p.running()

	opened, refused := dialAtOnce(t, []*process{p}, 2*access.MaxDialsPerInstance)

	assert.Equal(t, access.MaxDialsPerInstance, opened[p])
	assert.Equal(t, map[int]int{http.StatusTooManyRequests: access.MaxDialsPerInstance}, refused)
}

func TestDial_HoldsTheServersCapUnderDialsAtOnce(t *testing.T) {
	const maxDials, instances, each = 10, 3, 8
	h := newHarness(t, options{maxDials: maxDials})
	declared := h.bot()
	processes := make([]*process, 0, instances)
	for range instances {
		processes = append(processes, h.instance(declared))
	}
	processes[0].running()

	opened, refused := dialAtOnce(t, processes, each)

	total := 0
	for _, n := range opened {
		assert.LessOrEqual(t, n, access.MaxDialsPerInstance, "an instance holds no more than its bound")
		total += n
	}
	assert.Equal(t, maxDials, total, "the server holds its cap and fills it")
	assert.Equal(t, instances*each-maxDials, refused[http.StatusTooManyRequests]+refused[http.StatusServiceUnavailable],
		"every other dial is refused as a limit or the cap: %v", refused)
}

func TestDial_RefusesABody(t *testing.T) {
	h := newHarness(t, options{})
	p := h.instance(h.bot())
	p.running()

	status, _ := p.dialWith(t, `{"port":22}`)

	assert.Equal(t, http.StatusBadRequest, status)
	control, _ := p.dial(t)
	assert.Equal(t, http.StatusSwitchingProtocols, control)
}

func TestDial_GivesBackTheSlotOfAProviderThatDoesNotConnect(t *testing.T) {
	h := newHarness(t, options{maxDials: 1, dialTimeout: 100 * time.Millisecond})
	p := h.instance(h.bot())
	p.running()
	stuck := make(chan struct{})
	h.provider.set(func(p *provider) { p.stuckDial = stuck })
	t.Cleanup(func() { close(stuck) })

	start := time.Now()
	status, _ := p.dial(t)

	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Less(t, time.Since(start), 5*time.Second, "the dial waited no longer than its connect time")
	h.provider.set(func(p *provider) { p.stuckDial = nil })
	again, _ := p.dial(t)
	assert.Equal(t, http.StatusSwitchingProtocols, again, "the server's one slot was given back")
}
