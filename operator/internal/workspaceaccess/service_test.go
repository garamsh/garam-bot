package workspaceaccess_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closedWithin answers whether conn is closed within d: a read answers EOF or a closed connection
// rather than its deadline.
func closedWithin(t *testing.T, conn net.Conn, d time.Duration) bool {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(d)))
	_, err := conn.Read(make([]byte, 1))
	return err != nil && !errors.Is(err, os.ErrDeadlineExceeded)
}

// echoes answers whether conn carries a byte to the adapter and back, as workspacefake's port echoes.
func echoes(t *testing.T, conn net.Conn) bool {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	if _, err := conn.Write([]byte{7}); err != nil {
		return false
	}
	read := make([]byte, 1)
	_, err := io.ReadFull(conn, read)
	return err == nil && read[0] == 7
}

// openDial opens a dial of p's workspace, which the test requires open.
func (p *process) openDial(t *testing.T) net.Conn {
	t.Helper()
	p.running()
	status, conn := p.dial(t)
	require.Equal(t, http.StatusSwitchingProtocols, status)
	require.True(t, echoes(t, conn), "the dial is open")
	return conn
}

// stickGet holds every provider Get from now until the test ends, whatever its context says.
func (h *harness) stickGet() {
	stuck := make(chan struct{})
	h.provider.set(func(p *provider) { p.stuckGet = stuck })
	h.t.Cleanup(func() { close(stuck) })
}

func TestRun_ClosesAnIdleDialWhileAProviderReadIsStuck(t *testing.T) {
	h := newHarness(t, options{})
	conn := h.instance(h.bot()).openDial(t)
	h.stickGet()
	time.Sleep(100 * time.Millisecond) // a recheck is now stuck in Get
	require.False(t, closedWithin(t, conn, 100*time.Millisecond), "control: a dial not idle stays open")
	gets := h.provider.gets()
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, gets, h.provider.gets(), "a stuck read is not started again while it runs")

	h.advance(121 * time.Second)

	assert.True(t, closedWithin(t, conn, 2*time.Second), "the idle dial is closed (DIAL-3)")
}

func TestRun_ClosesADialWhosePodEndedWhileAProviderReadIsStuck(t *testing.T) {
	h := newHarness(t, options{bindingFresh: time.Nanosecond})
	p := h.instance(h.bot())
	conn := p.openDial(t)
	h.stickGet()
	time.Sleep(100 * time.Millisecond)
	require.False(t, closedWithin(t, conn, 100*time.Millisecond), "control: the dial is open while its Pod runs")

	h.api.endPod(p.podName)

	assert.True(t, closedWithin(t, conn, 2*time.Second), "the dial is closed (DIAL-4)")
}

func TestRun_ClosesADialWhoseAuthorityEnded(t *testing.T) {
	tests := []struct {
		name string
		end  func(h *harness, p *process)
	}{
		{"its Bot made again under its name", func(h *harness, _ *process) { h.bot() }},
		{"its Pod made again under its name", func(h *harness, p *process) {
			pod := p.pod
			pod.UID = "pod-again"
			h.api.setPod(p.podName, pod)
		}},
		{"its runtime container restarted", func(h *harness, p *process) {
			pod := p.pod
			pod.Container = "containerd://restarted"
			h.api.setPod(p.podName, pod)
		}},
		{"its workspace deleted", func(h *harness, _ *process) {
			all, err := h.provider.List(context.Background())
			require.NoError(t, err)
			require.Len(t, all, 1)
			require.NoError(t, h.provider.Delete(context.Background(), all[0].Name))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, options{bindingFresh: time.Nanosecond})
			p := h.instance(h.bot())
			conn := p.openDial(t)
			require.False(t, closedWithin(t, conn, 100*time.Millisecond), "control: the dial is open")

			tt.end(h, p)

			assert.True(t, closedWithin(t, conn, 2*time.Second), "the dial is closed (DIAL-4)")
		})
	}
}
