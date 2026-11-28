package reference

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// The files serve and the fixture subcommands share in the directory.
const (
	// TrustFile holds the serving certificate, PEM, for a client to trust (T-1).
	TrustFile = "ca.pem"
	// URLFile holds the base URL the server is reached at.
	URLFile = "url"
	// ControlFile holds the base URL of the fixture's control listener.
	ControlFile = "control"
	// CredentialKeyFile holds the key the place's credentials are signed with, readable by this user
	// alone.
	CredentialKeyFile = "credential-key.pem"
	// StartedFile holds when the server started, in Unix seconds, for the fixture to issue a
	// credential before it.
	StartedFile = "started"
)

const usage = `usage: workspace-access-reference --dir DIR <subcommand> [arguments]

  serve [--listen ADDR]
  credential INSTANCE [--audience AUDIENCE] [--expired] [--lifetime DURATION] [--before-start] [--no-iat]
  bot NAMESPACE NAME [--attachment A]... [--unsupported A]... [--tool T]...
  remake NAMESPACE NAME
  remove NAMESPACE NAME
  instance NAMESPACE NAME | instance --no-bot
  restart INSTANCE
  end INSTANCE
  workspace UID ATTACHMENT --stopped|--failed|--pending|--deleting|--running
  seed UID ATTACHMENT PATH CONTENT
  file UID ATTACHMENT PATH
  link UID ATTACHMENT PATH TARGET
  exists UID ATTACHMENT
  issuer --on|--off
  max-dials N
  clock --advance DURATION
  limits
`

// Main runs one subcommand: serve, or a fixture subcommand printing its answer as JSON. It answers the
// process's exit status.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("workspace-access-reference", flag.ContinueOnError)
	global.SetOutput(stderr)
	dir := global.String("dir", "", "the directory serve and the fixture subcommands share")
	if err := global.Parse(args); err != nil || *dir == "" || global.NArg() == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	name, rest := global.Arg(0), global.Args()[1:]
	var answer any
	var err error
	switch {
	case name == "serve":
		err = serve(ctx, *dir, rest, stdout, stderr)
	case name == "credential":
		answer, err = credential(*dir, rest)
	case controlled[name]:
		answer, err = forward(ctx, *dir, append([]string{name}, rest...))
	default:
		err = usageError(fmt.Sprintf("unknown subcommand %q", name))
	}
	var usageErr usageError
	switch {
	case errors.As(err, &usageErr):
		_, _ = fmt.Fprintf(stderr, "%s\n%s", usageErr, usage)
		return 2
	case err != nil:
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	case answer != nil:
		_ = json.NewEncoder(stdout).Encode(answer)
	}
	return 0
}

// usageError is a command line Main cannot run.
type usageError string

func (e usageError) Error() string { return string(e) }

// Ready is the line serve prints once it accepts.
type Ready struct {
	BaseURL   string `json:"baseUrl"`
	TrustFile string `json:"trustFile"`
}

func serve(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:0", "the address the protocol is served at")
	if err := flags.Parse(args); err != nil || flags.NArg() > 0 {
		return usageError("serve takes only --listen")
	}
	serving, certificate, err := servingCertificate()
	if err != nil {
		return err
	}
	credentialKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(credentialKey)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen at %s: %w", *listen, err)
	}
	defer func() { _ = listener.Close() }()
	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for the fixture: %w", err)
	}
	defer func() { _ = controlListener.Close() }()
	baseURL := "https://" + listener.Addr().String()
	trust := filepath.Join(dir, TrustFile)
	// A credential's iat is in whole seconds.
	started := time.Now().Truncate(time.Second)
	for path, content := range map[string][]byte{
		filepath.Join(dir, StartedFile):       []byte(strconv.FormatInt(started.Unix(), 10) + "\n"),
		trust:                                 pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}),
		filepath.Join(dir, URLFile):           []byte(baseURL + "\n"),
		filepath.Join(dir, ControlFile):       []byte("http://" + controlListener.Addr().String() + "\n"),
		filepath.Join(dir, CredentialKeyFile): pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}),
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	p := newPlace()
	s, err := newServer(p, baseURL, &credentialKey.PublicKey, started)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           s.handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{serving}, MinVersion: tls.VersionTLS13},
		ReadHeaderTimeout: 10 * time.Second,
	}
	control := &http.Server{Handler: p.control(), ReadHeaderTimeout: 10 * time.Second}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.closeIdle(ctx)
	failed := make(chan error, 2)
	go func() { failed <- server.ServeTLS(listener, "", "") }()
	go func() { failed <- control.Serve(controlListener) }()
	if err := json.NewEncoder(stdout).Encode(Ready{BaseURL: baseURL, TrustFile: trust}); err != nil {
		return fmt.Errorf("print the ready line: %w", err)
	}
	select {
	case <-ctx.Done():
	case err = <-failed:
	}
	shutdown, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	_ = control.Shutdown(shutdown)
	_ = server.Shutdown(shutdown)
	p.mu.Lock()
	p.closeDials(func(*dial) bool { return true })
	p.mu.Unlock()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// servingCertificate is a certificate for 127.0.0.1 and localhost that signs itself, and its DER.
func servingCertificate() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "workspace-access-reference"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, der, nil
}

// forward runs a subcommand on the running server's control listener, and answers what it printed.
func forward(ctx context.Context, dir string, args []string) (any, error) {
	read, err := os.ReadFile(filepath.Join(dir, ControlFile))
	if err != nil {
		return nil, fmt.Errorf("no server runs from %s: %w", dir, err)
	}
	body, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(string(read))+"/fixture", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, usageError(strings.TrimSpace(string(answer)))
	}
	return json.RawMessage(answer), nil
}

// CredentialAnswer is what the credential subcommand prints.
type CredentialAnswer struct {
	Credential string `json:"credential"`
}

// credential signs a place credential naming a workload instance, as the place issues one.
func credential(dir string, args []string) (any, error) {
	flags := flag.NewFlagSet("credential", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	audience := flags.String("audience", Audience, "")
	expired := flags.Bool("expired", false, "")
	lifetime := flags.Duration("lifetime", 10*time.Minute, "")
	beforeStart := flags.Bool("before-start", false, "")
	noIAT := flags.Bool("no-iat", false, "")
	positional, err := parseInterleaved(flags, args)
	if err != nil || len(positional) != 1 {
		return nil, usageError("credential takes INSTANCE, --audience, --expired, --lifetime, --before-start and --no-iat")
	}
	read, err := os.ReadFile(filepath.Join(dir, CredentialKeyFile))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(read)
	if block == nil {
		return nil, fmt.Errorf("%s holds no PEM key", CredentialKeyFile)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "reference"))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	expiry := now.Add(*lifetime)
	if *expired || *beforeStart {
		started, err := readStarted(dir)
		if err != nil {
			return nil, err
		}
		if *expired {
			// Issued as the server started, so only its expiry refuses it. Times are whole seconds, so
			// the expiry a second ago needs the server two seconds old.
			time.Sleep(time.Until(started.Add(2*time.Second + 100*time.Millisecond)))
			now, expiry = started, time.Now().Add(-time.Second)
		} else {
			// Issued a second before the server started, and alive for ten minutes from now.
			now, expiry = started.Add(-time.Second), time.Now().Add(10*time.Minute)
		}
	}
	claims := jwt.Claims{
		Issuer: issuer, Subject: positional[0], Audience: jwt.Audience{*audience},
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(expiry),
		ID: rand.Text(),
	}
	if *noIAT {
		claims.IssuedAt = nil
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return nil, err
	}
	return CredentialAnswer{Credential: raw}, nil
}

// readStarted is when the server in dir started.
func readStarted(dir string) (time.Time, error) {
	read, err := os.ReadFile(filepath.Join(dir, StartedFile))
	if err != nil {
		return time.Time{}, err
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(read)), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", StartedFile, err)
	}
	return time.Unix(seconds, 0), nil
}

// parseInterleaved parses flags wherever they stand among the positional arguments.
func parseInterleaved(flags *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		if flags.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
}
