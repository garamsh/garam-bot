package podworkspace

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/streaming/pkg/httpstream"
	httpstreamspdy "k8s.io/streaming/pkg/httpstream/spdy"
)

// silentServer accepts connections and reads what they send, and answers nothing: an API server
// whose port-forward never answers.
func silentServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	var read atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := conn.Read(buf)
					read.Add(int32(n))
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), &read
}

func silentGuest(t *testing.T, address string) *clusterGuest {
	t.Helper()
	config := &rest.Config{Host: "http://" + address}
	kube, err := kubernetes.NewForConfig(config)
	require.NoError(t, err)
	return &clusterGuest{rest: config, kube: kube, namespace: testNamespace}
}

// dialWithin dials through guest under ctx, and fails the test where the dial is not answered
// within 10 seconds.
func dialWithin(t *testing.T, ctx context.Context, guest *clusterGuest) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		conn, err := guest.dial(ctx, "ws-1", 7700)
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the dial was not answered in 10 seconds")
		return nil
	}
}

func TestClusterGuestDialEndsWithItsContextWhereThePortForwardNeverAnswers(t *testing.T) {
	address, read := silentServer(t)
	guest := silentGuest(t, address)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)

	err := dialWithin(t, ctx, guest)
	assert.ErrorIs(t, err, context.Canceled, "the dial ends with its context")
	// The case is the one named: the upgrade reached the server and went unanswered, rather than a
	// connect that failed.
	assert.Positive(t, read.Load(), "the server read no upgrade request")
}

func TestClusterGuestDialEndsAtItsTimeoutWhereThePortForwardNeverAnswersAndTheContextAllowsMore(t *testing.T) {
	address, read := silentServer(t)
	guest := silentGuest(t, address)
	guest.dialTimeout = 200 * time.Millisecond

	err := dialWithin(t, t.Context(), guest)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "the dial ends at its timeout")
	assert.Positive(t, read.Load(), "the server read no upgrade request")
}

// unansweringKubelet upgrades a port-forward to SPDY, as an API server does, and never replies to a
// stream: a kubelet that never answers. It counts the upgrades.
func unansweringKubelet(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var upgraded atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hold := func(httpstream.Stream, <-chan struct{}) error {
			<-release
			return nil
		}
		conn := httpstreamspdy.NewResponseUpgrader().UpgradeResponse(w, r, hold)
		if conn == nil {
			return
		}
		upgraded.Add(1)
		<-conn.CloseChan()
	}))
	t.Cleanup(func() {
		close(release)
		server.CloseClientConnections()
		server.Close()
	})
	return server.Listener.Addr().String(), &upgraded
}

func TestClusterGuestDialEndsWithItsContextWhereTheKubeletNeverAnswersAStream(t *testing.T) {
	address, upgraded := unansweringKubelet(t)
	guest := silentGuest(t, address)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)

	err := dialWithin(t, ctx, guest)
	assert.ErrorIs(t, err, context.Canceled, "the dial ends with its context")
	assert.Equal(t, int32(1), upgraded.Load(), "the case is the one named: the upgrade was answered")
}
