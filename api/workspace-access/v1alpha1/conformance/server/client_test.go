//go:build conformance

package server

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
)

// The environment a run is given.
const (
	EnvBaseURL   = "CONFORMANCE_BASE_URL"
	EnvTrustFile = "CONFORMANCE_TRUST_FILE"
	EnvFixture   = "CONFORMANCE_FIXTURE"
)

var (
	baseURL string
	roots   *x509.CertPool
	client  *http.Client
	fixture []string
)

func TestMain(m *testing.M) {
	baseURL = strings.TrimRight(os.Getenv(EnvBaseURL), "/")
	fixture = strings.Fields(os.Getenv(EnvFixture))
	trust, err := os.ReadFile(os.Getenv(EnvTrustFile))
	if baseURL == "" || len(fixture) == 0 || err != nil {
		fmt.Fprintf(os.Stderr, "%s, %s and %s name the server under test\n", EnvBaseURL, EnvTrustFile, EnvFixture)
		os.Exit(2)
	}
	roots = x509.NewCertPool()
	if !roots.AppendCertsFromPEM(trust) {
		fmt.Fprintf(os.Stderr, "%s holds no PEM certificate\n", os.Getenv(EnvTrustFile))
		os.Exit(2)
	}
	client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}}
	os.Exit(m.Run())
}

// process stands in for a bot's process: it holds its key and signs proofs with it.
type process struct {
	key *ecdsa.PrivateKey
}

func newProcess(t *testing.T) process {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return process{key: key}
}

// proofOptions change a proof from the one a process signs for its request.
type proofOptions struct {
	method, url, credential string
	iat                     time.Duration
	reuse                   string
}

// proof signs a proof for method and target with credential's hash.
func (p process) proof(t *testing.T, method, target, credential string, o proofOptions) string {
	t.Helper()
	if o.reuse != "" {
		return o.reuse
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: p.key},
		(&jose.SignerOptions{EmbedJWK: true}).WithType("dpop+jwt"))
	require.NoError(t, err)
	if o.method != "" {
		method = o.method
	}
	if o.url != "" {
		target = o.url
	}
	if o.credential != "" {
		credential = o.credential
	}
	digest := sha256.Sum256([]byte(credential))
	raw, err := jwt.Signed(signer).Claims(map[string]any{
		"htm": method, "htu": target, "iat": time.Now().Add(o.iat).Unix(), "jti": rand.Text(),
		"ath": base64.RawURLEncoding.EncodeToString(digest[:]),
	}).Serialize()
	require.NoError(t, err)
	return raw
}

// answered is one call's answer.
type answered struct {
	status int
	code   v1alpha1.Code
	raw    string
	header http.Header
}

// call makes one call of the protocol as p, with credential, and answers it. A refusal must be an
// ErrorBody naming a known code (T-3).
func (p process) call(t *testing.T, credential, method, path string, body any, o proofOptions) answered {
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(b)
	default:
		encoded, err := json.Marshal(b)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, baseURL+path, reader)
	require.NoError(t, err)
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
		request.Header.Set(v1alpha1.HeaderProof, p.proof(t, method, baseURL+path, credential, o))
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	read, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	a := answered{status: response.StatusCode, raw: string(read), header: response.Header}
	if a.status >= 400 {
		var refusal v1alpha1.ErrorBody
		require.NoError(t, json.Unmarshal(read, &refusal), "a refusal is ErrorBody (T-3): %s", read)
		require.Contains(t, v1alpha1.Codes, refusal.Error, "a refusal names a known code (T-3)")
		a.code = refusal.Error
	}
	return a
}

// bot is one bot the fixture placed, with one running instance, its credential and its process.
type bot struct {
	namespace, name, uid string
	instance, credential string
	process              process
}

var bots atomic.Int32

// newBot places a bot of a namespace of its own with the attachment main and the fixture arguments
// args, starts an instance of it, and answers it.
func newBot(t *testing.T, args ...string) *bot {
	t.Helper()
	namespace := fmt.Sprintf("conformance-%d", bots.Add(1))
	var placed struct {
		UID string `json:"uid"`
	}
	fixtureJSON(t, &placed, append([]string{"bot", namespace, "mira", "--attachment", "main"}, args...)...)
	b := &bot{namespace: namespace, name: "mira", uid: placed.UID, process: newProcess(t)}
	b.instance = instance(t, namespace, "mira")
	b.credential = credential(t, b.instance)
	return b
}

func (b *bot) call(t *testing.T, method, path string, body any) answered {
	t.Helper()
	return b.process.call(t, b.credential, method, path, body, proofOptions{})
}

func (b *bot) acquire(t *testing.T, attachment string) answered {
	t.Helper()
	return b.call(t, http.MethodPut, v1alpha1.WorkspacesPath+attachment, nil)
}

// running acquires main and requires it running.
func (b *bot) running(t *testing.T) {
	t.Helper()
	a := b.acquire(t, "main")
	require.Equal(t, http.StatusOK, a.status, a.raw)
	require.EqualValues(t, "running", workspaceOf(t, a).Phase)
}

func (b *bot) write(t *testing.T, files ...v1alpha1.File) answered {
	t.Helper()
	return b.call(t, http.MethodPut, v1alpha1.WorkspacesPath+"main"+v1alpha1.FilesSuffix, v1alpha1.Files{Files: files})
}

// file reads a path of the bot's workspace main through the fixture.
func (b *bot) file(t *testing.T, path string) fileState {
	t.Helper()
	var f fileState
	fixtureJSON(t, &f, "file", b.uid, "main", path)
	if f.Content != "" {
		decoded, err := base64.StdEncoding.DecodeString(f.Content)
		require.NoError(t, err)
		f.Content = string(decoded)
	}
	return f
}

type fileState struct {
	Exists  bool   `json:"exists"`
	Content string `json:"content"`
	Link    string `json:"link"`
}

func workspaceOf(t *testing.T, a answered) v1alpha1.Workspace {
	t.Helper()
	var w v1alpha1.Workspace
	require.NoError(t, json.Unmarshal([]byte(a.raw), &w), a.raw)
	return w
}

// dialed is a dial's outcome: its connection where it upgraded, and its answer otherwise.
type dialed struct {
	conn   net.Conn
	answer answered
}

// dial opens a dial of attachment as b, over HTTP/1.1 alone, with upgrade as its Upgrade token.
func (b *bot) dial(t *testing.T, attachment, upgrade string) dialed {
	t.Helper()
	target := baseURL + v1alpha1.WorkspacesPath + attachment + v1alpha1.DialSuffix
	u, err := url.Parse(target)
	require.NoError(t, err)
	conn, err := tls.Dial("tcp", u.Host, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}, ServerName: u.Hostname()})
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+b.credential)
	request.Header.Set(v1alpha1.HeaderProof, b.process.proof(t, http.MethodGet, target, b.credential, proofOptions{}))
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", upgrade)
	require.NoError(t, request.Write(conn))
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	require.NoError(t, err)
	if response.StatusCode != http.StatusSwitchingProtocols {
		defer conn.Close()
		read, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		var refusal v1alpha1.ErrorBody
		require.NoError(t, json.Unmarshal(read, &refusal), "a refusal is ErrorBody (T-3): %s", read)
		return dialed{answer: answered{status: response.StatusCode, code: refusal.Error, raw: string(read), header: response.Header}}
	}
	require.True(t, strings.EqualFold(response.Header.Get("Upgrade"), v1alpha1.DialProtocol), "the 101 names the protocol (DIAL-1)")
	require.True(t, strings.EqualFold(response.Header.Get("Connection"), "Upgrade"), "the 101 names Connection: Upgrade (DIAL-1)")
	opened := &bufferedConn{Conn: conn, reader: reader}
	t.Cleanup(func() { _ = opened.Close() })
	return dialed{conn: opened, answer: answered{status: response.StatusCode, header: response.Header}}
}

// bufferedConn reads what the response's reader already holds before the connection.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// echoes sends a line over conn and requires it back.
func echoes(t *testing.T, conn net.Conn) bool {
	t.Helper()
	line := []byte("ping " + rand.Text() + "\n")
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	if _, err := conn.Write(line); err != nil {
		return false
	}
	got := make([]byte, len(line))
	if _, err := io.ReadFull(conn, got); err != nil {
		return false
	}
	return bytes.Equal(got, line)
}

// closes requires conn to be closed by the server within 3 seconds.
func closes(t *testing.T, conn net.Conn) bool {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	_, err := conn.Read(make([]byte, 1))
	return err == io.EOF || (err != nil && !isTimeout(err))
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// run runs one fixture subcommand, and answers what it printed.
func run(t *testing.T, args ...string) []byte {
	t.Helper()
	command := exec.Command(fixture[0], append(slices.Clone(fixture[1:]), args...)...) //nolint:gosec
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	require.NoError(t, err, "fixture %v: %s", args, stderr.String())
	return out
}

func fixtureJSON(t *testing.T, into any, args ...string) {
	t.Helper()
	require.NoError(t, json.Unmarshal(run(t, args...), into))
}

func instance(t *testing.T, args ...string) string {
	t.Helper()
	var answer struct {
		Instance string `json:"instance"`
	}
	fixtureJSON(t, &answer, append([]string{"instance"}, args...)...)
	require.NotEmpty(t, answer.Instance)
	return answer.Instance
}

func credential(t *testing.T, args ...string) string {
	t.Helper()
	var answer struct {
		Credential string `json:"credential"`
	}
	fixtureJSON(t, &answer, append([]string{"credential"}, args...)...)
	require.NotEmpty(t, answer.Credential)
	return answer.Credential
}
