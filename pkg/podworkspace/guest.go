package podworkspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	utilexec "k8s.io/client-go/util/exec"
	"k8s.io/streaming/pkg/httpstream"
	httpstreamspdy "k8s.io/streaming/pkg/httpstream/spdy"

	"github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// The exit codes of the file helper (cmd/workspacefile): a read of a path that holds no file, a write
// whose path passes through a link, and a write the node's kernel cannot make without following one.
const (
	exitFileNotFound = 3
	exitLink         = 4
	exitUnsupported  = 5
)

// guest reaches into a workspace's Pod: the two calls of the Kubernetes API that are not plain
// reads and writes of objects.
type guest interface {
	// exec runs command in the workspace container with stdin and returns its result whole.
	exec(ctx context.Context, pod string, command []string, stdin []byte) (v1alpha1.ExecResult, error)
	// dial opens a connection to a TCP port of the Pod.
	dial(ctx context.Context, pod string, port uint16) (net.Conn, error)
}

// running returns the workspace's spec when it runs, ErrNotRunning when it does not.
func (p *Provider) running(ctx context.Context, name v1alpha1.Name) (v1alpha1.Spec, error) {
	ws, err := p.Get(ctx, name)
	if err != nil {
		return v1alpha1.Spec{}, err
	}
	if ws.Status.Phase != v1alpha1.PhaseRunning {
		return v1alpha1.Spec{}, fmt.Errorf("%w: %s is %s", v1alpha1.ErrNotRunning, name, ws.Status.Phase)
	}

	return ws.Spec, nil
}

// Exec runs one command in the workspace (EXEC-1). A command that outlives req.Timeout is an
// error, not a result: the interface leaves that case open.
func (p *Provider) Exec(ctx context.Context, name v1alpha1.Name, req v1alpha1.ExecRequest) (v1alpha1.ExecResult, error) {
	if len(req.Command) == 0 {
		return v1alpha1.ExecResult{}, fmt.Errorf("%w: no command", v1alpha1.ErrInvalid)
	}
	if _, err := p.running(ctx, name); err != nil {
		return v1alpha1.ExecResult{}, err
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	return p.guest.exec(ctx, string(name), req.Command, req.Stdin)
}

// ReadFile returns the file at path, read by the helper (FILE-1, FILE-2).
func (p *Provider) ReadFile(ctx context.Context, name v1alpha1.Name, path string) ([]byte, error) {
	if err := v1alpha1.CheckPath(path); err != nil {
		return nil, err
	}
	if _, err := p.running(ctx, name); err != nil {
		return nil, err
	}
	res, err := p.guest.exec(ctx, string(name), []string{helperPath, "read", path}, nil)
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
		return res.Stdout, nil
	case exitFileNotFound:
		return nil, fmt.Errorf("%w: %s", v1alpha1.ErrFileNotFound, path)
	}

	return nil, fmt.Errorf("read %s: helper exit %d: %s", path, res.ExitCode, bytes.TrimSpace(res.Stderr))
}

// WriteFile replaces the file at path with content, written by the helper, which follows no link
// (FILE-1, FILE-3).
func (p *Provider) WriteFile(ctx context.Context, name v1alpha1.Name, path string, content []byte) error {
	if err := v1alpha1.CheckPath(path); err != nil {
		return err
	}
	if _, err := p.running(ctx, name); err != nil {
		return err
	}
	res, err := p.guest.exec(ctx, string(name), []string{helperPath, "write", path}, content)
	if err != nil {
		return err
	}
	switch res.ExitCode {
	case 0:
		return nil
	case exitLink:
		return fmt.Errorf("%w: %s passes through a symbolic link", v1alpha1.ErrInvalid, path)
	case exitUnsupported:
		return fmt.Errorf("%w: write %s: %s", v1alpha1.ErrUnsupported, path, bytes.TrimSpace(res.Stderr))
	}

	return fmt.Errorf("write %s: helper exit %d: %s", path, res.ExitCode, bytes.TrimSpace(res.Stderr))
}

// Dial opens a connection to one of the spec's ports (DIAL-1).
func (p *Provider) Dial(ctx context.Context, name v1alpha1.Name, port uint16) (net.Conn, error) {
	spec, err := p.running(ctx, name)
	if err != nil {
		return nil, err
	}
	if !containsPort(spec.Ports, port) {
		return nil, fmt.Errorf("%w: port %d is not one of the spec's", v1alpha1.ErrInvalid, port)
	}

	return p.guest.dial(ctx, string(name), port)
}

func containsPort(ports []uint16, port uint16) bool {
	for _, p := range ports {
		if p == port {
			return true
		}
	}

	return false
}

// clusterGuest is the guest of a real cluster.
type clusterGuest struct {
	rest      *rest.Config
	kube      kubernetes.Interface
	namespace string
	// dialTimeout bounds a dial, defaultDialTimeout where zero.
	dialTimeout time.Duration
}

func (g *clusterGuest) exec(ctx context.Context, pod string, command []string, stdin []byte) (v1alpha1.ExecResult, error) {
	req := g.kube.CoreV1().RESTClient().Post().Resource("pods").Namespace(g.namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: containerName, Command: command, Stdin: true, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(g.rest, http.MethodPost, req.URL())
	if err != nil {
		return v1alpha1.ExecResult{}, fmt.Errorf("exec in %s: %w", pod, err)
	}
	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: bytes.NewReader(stdin), Stdout: &stdout, Stderr: &stderr})
	res := v1alpha1.ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exit utilexec.CodeExitError
	switch {
	case err == nil:
		return res, nil
	case errors.As(err, &exit):
		res.ExitCode = int32(exit.Code)
		return res, nil
	}

	return v1alpha1.ExecResult{}, fmt.Errorf("exec in %s: %w", pod, err)
}

// defaultDialTimeout bounds a dial's connect, upgrade and streams, whatever ctx allows.
const defaultDialTimeout = 30 * time.Second

// dial opens the port-forward streams of one port and bridges them to one end of an in-memory
// pipe, which it returns the other end of: that end takes deadlines as a socket does, and nothing
// listens on the caller's host. ctx, and the guest's dial timeout, bound the connect, the upgrade
// and the streams' creation: an API server that never answers is left, its connection closed. The
// connection returned outlives ctx.
func (g *clusterGuest) dial(ctx context.Context, pod string, port uint16) (net.Conn, error) {
	timeout := g.dialTimeout
	if timeout == 0 {
		timeout = defaultDialTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	session, err := g.forward(ctx, pod)
	if err != nil {
		return nil, fmt.Errorf("port-forward to %s: %w", pod, err)
	}
	errStream, dataStream, err := openStreams(ctx, session, port)
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("port-forward to %s: %w", pod, err)
	}

	caller, bridge := net.Pipe()
	go func() {
		// The kubelet writes the reason a forward failed to the error stream and closes it.
		if msg, _ := io.ReadAll(errStream); len(msg) > 0 {
			_ = bridge.Close()
		}
	}()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(dataStream, bridge)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(bridge, dataStream)
		done <- struct{}{}
	}()
	go func() {
		// The first side to end ends the connection: the pipe is closed by its caller or by the
		// copy above, and the pipe carries every byte the other side sent before it ended.
		<-done
		_ = bridge.Close()
		_ = dataStream.Close()
		_ = session.Close()
	}()

	return caller, nil
}

// openStreams creates the error and data streams of port on session. A stream's creation waits for
// the kubelet's reply up to the session's own 30 seconds and not for a closed session, so it is made
// aside and left where ctx ends first; it ends at that timeout at the latest.
func openStreams(ctx context.Context, session httpstream.Connection, port uint16) (httpstream.Stream, httpstream.Stream, error) {
	type opened struct {
		errStream, dataStream httpstream.Stream
		err                   error
	}
	done := make(chan opened, 1)
	go func() {
		headers := http.Header{}
		headers.Set(corev1.PortHeader, strconv.Itoa(int(port)))
		headers.Set(corev1.PortForwardRequestIDHeader, "0")
		headers.Set(corev1.StreamType, corev1.StreamTypeError)
		errStream, err := session.CreateStream(headers)
		if err != nil {
			done <- opened{err: fmt.Errorf("error stream: %w", err)}
			return
		}
		_ = errStream.Close()
		headers.Set(corev1.StreamType, corev1.StreamTypeData)
		dataStream, err := session.CreateStream(headers)
		if err != nil {
			done <- opened{err: fmt.Errorf("data stream: %w", err)}
			return
		}
		done <- opened{errStream: errStream, dataStream: dataStream}
	}()
	select {
	case got := <-done:
		return got.errStream, got.dataStream, got.err
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

// forward upgrades a port-forward request of pod to a SPDY session, its connection closed if ctx
// ends before the session is made. The upgrade dials the API server itself and reads its answer
// with no regard for ctx, so its round tripper is given a transport whose dial records the
// connection, to be closed.
func (g *clusterGuest) forward(ctx context.Context, pod string) (httpstream.Connection, error) {
	tlsConfig, err := rest.TLSConfigFor(g.rest)
	if err != nil {
		return nil, err
	}
	var (
		mu     sync.Mutex
		conns  []net.Conn
		closed bool
	)
	var connector net.Dialer
	upgrade := &http.Transport{
		TLSClientConfig: tlsConfig,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := connector.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			if closed { // ctx ended while the connect was answered
				_ = conn.Close()
				return nil, context.Cause(ctx)
			}
			conns = append(conns, conn)
			return conn, nil
		},
	}
	upgrader, err := httpstreamspdy.NewRoundTripperWithConfig(httpstreamspdy.RoundTripperConfig{
		PingPeriod: 5 * time.Second, UpgradeTransport: upgrade,
	})
	if err != nil {
		return nil, err
	}
	wrapped, err := rest.HTTPWrappersForConfig(g.rest, upgrader)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		closed = true
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	defer stop()
	url := g.kube.CoreV1().RESTClient().Post().Resource("pods").Namespace(g.namespace).Name(pod).SubResource("portforward").URL()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url.String(), nil)
	if err != nil {
		return nil, err
	}
	session, _, err := spdy.NegotiateStreaming(spdy.NewUpgraderForStreaming(upgrader), &http.Client{Transport: wrapped}, req,
		portforward.PortForwardProtocolV1Name)
	if ctx.Err() != nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, ctx.Err()
	}
	return session, err
}
