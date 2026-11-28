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
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const usage = `usage: bot-token-reference --dir DIR <subcommand> [arguments]

  serve [--listen ADDR]
  credential NAMESPACE NAME [--audience AUDIENCE] [--expired]
  bot NAMESPACE NAME --owner garam|cluster
  binding ACCESS_TOKEN
`

// Main runs one subcommand: serve, or a fixture subcommand printing its answer as JSON. It answers the
// process's exit status.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("bot-token-reference", flag.ContinueOnError)
	global.SetOutput(stderr)
	dir := global.String("dir", "", "the directory serve and the fixture subcommands share")
	if err := global.Parse(args); err != nil || *dir == "" || global.NArg() == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	name, rest := global.Arg(0), global.Args()[1:]
	var answer any
	var err error
	switch name {
	case "serve":
		err = serve(ctx, *dir, rest, stdout, stderr)
	case "credential":
		answer, err = credential(*dir, rest)
	case "bot":
		answer, err = bot(*dir, rest)
	case "binding":
		answer, err = binding(*dir, rest)
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
	baseURL := "https://" + listener.Addr().String()
	trust := filepath.Join(dir, TrustFile)
	for path, content := range map[string][]byte{
		trust:                                 pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}),
		filepath.Join(dir, URLFile):           []byte(baseURL + "\n"),
		filepath.Join(dir, CredentialKeyFile): pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}),
	} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			_ = listener.Close()
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	server := &http.Server{
		Handler:           newServer(dir, baseURL, &credentialKey.PublicKey).handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{serving}, MinVersion: tls.VersionTLS13},
		ReadHeaderTimeout: 10 * time.Second,
	}
	failed := make(chan error, 1)
	go func() { failed <- server.ServeTLS(listener, "", "") }()
	if err := json.NewEncoder(stdout).Encode(Ready{BaseURL: baseURL, TrustFile: trust}); err != nil {
		return fmt.Errorf("print the ready line: %w", err)
	}
	select {
	case <-ctx.Done():
	case err = <-failed:
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
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
		Subject:      pkix.Name{CommonName: "bot-token-reference"},
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

// CredentialAnswer is what the credential subcommand prints.
type CredentialAnswer struct {
	Credential string `json:"credential"`
}

// credential signs a place credential naming the bot namespace/name, as the place issues one.
func credential(dir string, args []string) (any, error) {
	flags := flag.NewFlagSet("credential", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	audience := flags.String("audience", Audience, "")
	expired := flags.Bool("expired", false, "")
	positional, err := parseInterleaved(flags, args)
	if err != nil || len(positional) != 2 {
		return nil, usageError("credential takes NAMESPACE NAME, --audience and --expired")
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
	expiry := now.Add(10 * time.Minute)
	if *expired {
		now, expiry = now.Add(-20*time.Minute), now.Add(-10*time.Minute)
	}
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: issuer, Subject: positional[0] + "/" + positional[1], Audience: jwt.Audience{*audience},
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(expiry),
		ID: rand.Text(),
	}).Serialize()
	if err != nil {
		return nil, err
	}
	return CredentialAnswer{Credential: raw}, nil
}

// bot places namespace/name at the place, owned as --owner says.
func bot(dir string, args []string) (any, error) {
	flags := flag.NewFlagSet("bot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	owner := flags.String("owner", "", "")
	positional, err := parseInterleaved(flags, args)
	if err != nil || len(positional) != 2 || (Owner(*owner) != OwnerGaram && Owner(*owner) != OwnerCluster) {
		return nil, usageError("bot takes NAMESPACE NAME and --owner garam|cluster")
	}
	bots, err := readBots(dir)
	if err != nil {
		return nil, err
	}
	bots[positional[0]+"/"+positional[1]] = Owner(*owner)
	encoded, err := json.Marshal(bots)
	if err != nil {
		return nil, err
	}
	staged, err := os.CreateTemp(dir, BotsFile+".*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(staged.Name()) }()
	if _, err := io.Copy(staged, bytes.NewReader(encoded)); err != nil {
		_ = staged.Close()
		return nil, err
	}
	if err := staged.Close(); err != nil {
		return nil, err
	}
	// A rename, so the server never reads a partial file.
	if err := os.Rename(staged.Name(), filepath.Join(dir, BotsFile)); err != nil {
		return nil, err
	}
	return map[string]string{"namespace": positional[0], "name": positional[1], "owner": *owner}, nil
}

// BindingAnswer is what the binding subcommand prints: the RFC 7638 thumbprint of the key a token is
// bound to.
type BindingAnswer struct {
	JKT string `json:"jkt"`
}

func binding(dir string, args []string) (any, error) {
	if len(args) != 1 {
		return nil, usageError("binding takes ACCESS_TOKEN")
	}
	read, err := os.ReadFile(filepath.Join(dir, BindingsDir, bindingName(args[0])))
	if err != nil {
		return nil, fmt.Errorf("no token of that value was answered: %w", err)
	}
	return BindingAnswer{JKT: string(read)}, nil
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
