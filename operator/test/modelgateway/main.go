//go:build e2e

// Command modelgateway serves the cluster's model gateway, internal/modelgateway, over stand-ins for
// the API server's keys and Secrets, and is the fixture interface 5's server suite drives it by
// (api/model-gateway/v1alpha1/conformance.md). Its upstream is the suite's, gatewayfake.Provider. It
// is built for that run alone, under the build tag e2e, and no release carries it.
//
//	modelgateway --dir DIR serve [--base-path PATH]
//	modelgateway --dir DIR <fixture subcommand> [arguments]
package main

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
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"k8s.io/client-go/kubernetes/fake"

	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
	"github.com/garamsh/garam-bot/api/model-gateway/v1alpha1/gatewayfake"

	"github.com/garamsh/garam-bot/operator/internal/modelgateway"
	gatewaykube "github.com/garamsh/garam-bot/operator/internal/modelgateway/kube"
)

// The files serve writes in its directory for the suite and the fixture.
const (
	trustFile   = "ca.pem"
	urlFile     = "url"
	controlFile = "control"
)

// seen is the fixture subcommand, and the answer's field, naming what the provider received.
const seen = "seen"

// The harness's choices, which `limits` and `credential` print (conformance.md §The fixture).
const (
	namespace   = "garam-system"
	connector   = "garam-connector"
	apiIssuer   = "https://kubernetes.default.svc.cluster.local"
	keyID       = "api-server-key"
	secretName  = "garam-model-gateway-signing-key"
	providerKey = "sk-harness-provider-key-0123456789"
	// Each bot's limit is small, so LIMIT-1 is reached in a few calls; the issuer's is room for every
	// case's tokens.
	botBurst    = 5
	botEvery    = 2 * time.Second
	issuerBurst = 50
	issuerEvery = time.Second
)

const usage = `usage: modelgateway --dir DIR <subcommand> [arguments]

  serve [--base-path PATH]
  credential [--issuer NAME] [--audience AUDIENCE] [--expired] [--lifetime DURATION] [--foreign]
  token --namespace NS --name NAME --uid UID [--foreign]
  provider-key
  seen [--redirected]
  answer [--status N] [--retry-after SECONDS] [--stream] [--long] [--break] [--redirect STATUS] [--close]
  release
  usage
  restart
  clock --advance DURATION
  limits`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("modelgateway", flag.ContinueOnError)
	dir := flags.String("dir", "", "the directory serve and the fixture share")
	if err := flags.Parse(args); err != nil || *dir == "" || flags.NArg() == 0 {
		return errors.New(usage)
	}
	command, rest := flags.Arg(0), flags.Args()[1:]
	if command == "serve" {
		serveFlags := flag.NewFlagSet("serve", flag.ContinueOnError)
		basePath := serveFlags.String("base-path", "", "the path of the gateway's base URL (T-2)")
		if err := serveFlags.Parse(rest); err != nil {
			return err
		}
		return serve(*dir, *basePath)
	}
	body := map[string]string{}
	sub := flag.NewFlagSet(command, flag.ContinueOnError)
	value := func(name string) { sub.Func(name, "", func(v string) error { body[name] = v; return nil }) }
	set := func(name string) { sub.BoolFunc(name, "", func(string) error { body[name] = "true"; return nil }) }
	switch command {
	case "credential":
		value("issuer")
		value("audience")
		value("lifetime")
		set("expired")
		set("foreign")
	case "token":
		value("namespace")
		value("name")
		value("uid")
		set("foreign")
	case seen:
		set("redirected")
	case "answer":
		value("status")
		value("retry-after")
		value("redirect")
		set("stream")
		set("long")
		set("break")
		set("close")
	case "clock":
		value("advance")
	case "provider-key", "release", "usage", "restart", "limits":
	default:
		return errors.New(usage)
	}
	if err := sub.Parse(rest); err != nil {
		return err
	}
	control, err := os.ReadFile(filepath.Join(*dir, controlFile))
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(body)
	response, err := http.Post(string(control)+"/"+command, "application/json", bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	answer, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", command, answer)
	}
	_, err = out.Write(answer)
	return err
}

// apiServer stands in for the API server's ServiceAccount token issuer: it signs the place's
// credentials, and answers its keys as the gateway reads them.
type apiServer struct {
	key *ecdsa.PrivateKey
}

func (a apiServer) Keys(context.Context) (modelgateway.Issuer, error) {
	return modelgateway.Issuer{URL: apiIssuer, Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &a.key.PublicKey, KeyID: keyID, Algorithm: string(jose.ES256), Use: "sig"},
	}}}, nil
}

// credential is a projected ServiceAccount token of the ServiceAccount name in namespace.
func (a apiServer) credential(name, audience string, issuedAt time.Time, life time.Duration) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: a.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID))
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: apiIssuer, Subject: "system:serviceaccount:" + namespace + ":" + name, Audience: jwt.Audience{audience},
		IssuedAt: jwt.NewNumericDate(issuedAt), NotBefore: jwt.NewNumericDate(issuedAt),
		Expiry: jwt.NewNumericDate(issuedAt.Add(life)),
	}).Serialize()
}

// place is what serve runs: the gateway, restartable over the same Secrets, its upstream, and its
// clock. foreign is a gateway of another signing key, and foreignAPI an API server of another key.
type place struct {
	basePath   string
	provider   *gatewayfake.Provider
	elsewhere  *gatewayfake.Recorder
	upstream   *url.URL
	api        apiServer
	foreignAPI apiServer
	secrets    *fake.Clientset
	offset     atomic.Int64

	mu      sync.Mutex
	gateway *modelgateway.Gateway
	foreign *modelgateway.Gateway
	usage   []modelgateway.Usage
}

func (p *place) now() time.Time { return time.Now().Add(time.Duration(p.offset.Load())) }

func (p *place) record(u modelgateway.Usage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usage = append(p.usage, u)
}

func (p *place) config(secrets *fake.Clientset) modelgateway.Config {
	return modelgateway.Config{
		BasePath: p.basePath, Upstream: p.upstream, Key: providerKey,
		SigningKeys: gatewaykube.SigningKeys{Kube: secrets, Namespace: namespace, Name: secretName},
		Keys:        p.api, Issuer: namespace + "/" + connector, Record: p.record, Now: p.now,
		Limits: modelgateway.Limits{
			Bot:    modelgateway.Limit{Burst: botBurst, Every: botEvery},
			Issuer: modelgateway.Limit{Burst: issuerBurst, Every: issuerEvery},
		},
	}
}

// start makes the gateway again over the same Secrets, as a restart of its Pod does: the signing key
// it reads is the one the first start wrote (TOKEN-2).
func (p *place) start() error {
	g, err := modelgateway.New(context.Background(), p.config(p.secrets))
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.gateway = g
	p.mu.Unlock()
	return nil
}

func (p *place) current() *modelgateway.Gateway {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gateway
}

func newKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }

func serve(dir, basePath string) error {
	p := &place{
		basePath: basePath, provider: &gatewayfake.Provider{}, elsewhere: &gatewayfake.Recorder{},
		secrets: fake.NewClientset(),
	}
	elsewhere := httptest.NewServer(p.elsewhere)
	defer elsewhere.Close()
	p.provider.Elsewhere = elsewhere.URL
	upstream := httptest.NewServer(p.provider)
	defer upstream.Close()
	p.upstream, _ = url.Parse(upstream.URL)
	for _, into := range []*apiServer{&p.api, &p.foreignAPI} {
		key, err := newKey()
		if err != nil {
			return err
		}
		into.key = key
	}
	if err := p.start(); err != nil {
		return err
	}
	foreign, err := modelgateway.New(context.Background(), p.config(fake.NewClientset()))
	if err != nil {
		return err
	}
	p.foreign = foreign
	certificate, trust, err := selfSigned()
	if err != nil {
		return err
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0",
		&tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.current().ServeHTTP(w, r) }),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	control, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go func() { _ = (&http.Server{Handler: p.control(), ReadHeaderTimeout: 10 * time.Second}).Serve(control) }()
	for name, content := range map[string][]byte{
		trustFile:   trust,
		urlFile:     []byte("https://" + listener.Addr().String() + basePath),
		controlFile: []byte("http://" + control.Addr().String()),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return err
		}
	}
	fmt.Println("ready")
	select {}
}

// tokenOf obtains a model token for bot from g's issue surface, with the connector's credential.
func (p *place) tokenOf(g *modelgateway.Gateway, bot gateway.TokenRequest) (gateway.ModelToken, error) {
	credential, err := p.api.credential(connector, gateway.Audience, p.now(), 10*time.Minute)
	if err != nil {
		return gateway.ModelToken{}, err
	}
	body, _ := json.Marshal(bot)
	request := httptest.NewRequest(http.MethodPost, p.basePath+gateway.TokensPath, bytes.NewReader(body))
	request.Header.Set(gateway.HeaderAuthorization, "Bearer "+credential)
	answer := httptest.NewRecorder()
	g.ServeHTTP(answer, request)
	if answer.Code != http.StatusOK {
		return gateway.ModelToken{}, fmt.Errorf("the issue surface answered %d: %s", answer.Code, answer.Body)
	}
	var token gateway.ModelToken
	return token, json.Unmarshal(answer.Body.Bytes(), &token)
}

func (p *place) control() http.Handler {
	mux := http.NewServeMux()
	handle := func(name string, do func(map[string]string) (any, error)) {
		mux.HandleFunc("POST /"+name, func(w http.ResponseWriter, r *http.Request) {
			args := map[string]string{}
			_ = json.NewDecoder(r.Body).Decode(&args)
			answer, err := do(args)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(answer)
		})
	}
	handle("credential", func(a map[string]string) (any, error) {
		name, audience, issuedAt, life := connector, gateway.Audience, p.now(), 10*time.Minute
		if a["issuer"] != "" {
			name = a["issuer"]
		}
		if a["audience"] != "" {
			audience = a["audience"]
		}
		if a["lifetime"] != "" {
			parsed, err := time.ParseDuration(a["lifetime"])
			if err != nil {
				return nil, err
			}
			life = parsed
		}
		if a["expired"] != "" {
			issuedAt, life = p.now().Add(-20*time.Minute), 10*time.Minute
		}
		api := p.api
		if a["foreign"] != "" {
			api = p.foreignAPI
		}
		credential, err := api.credential(name, audience, issuedAt, life)
		return map[string]string{"credential": credential, "issuer": name}, err
	})
	handle("token", func(a map[string]string) (any, error) {
		g := p.current()
		if a["foreign"] != "" {
			g = p.foreign
		}
		return p.tokenOf(g, gateway.TokenRequest{Namespace: a["namespace"], Name: a["name"], UID: a["uid"]})
	})
	handle("provider-key", func(map[string]string) (any, error) { return map[string]string{"key": providerKey}, nil })
	handle(seen, func(a map[string]string) (any, error) {
		if a["redirected"] != "" {
			return map[string]any{seen: p.elsewhere.Seen()}, nil
		}
		return map[string]any{seen: p.provider.Seen()}, nil
	})
	handle("answer", func(a map[string]string) (any, error) {
		answer := gatewayfake.Answer{
			Stream: a["stream"] != "", Long: a["long"] != "", Break: a["break"] != "", Close: a["close"] != "",
			RetryAfter: a["retry-after"],
		}
		for name, into := range map[string]*int{"status": &answer.Status, "redirect": &answer.Redirect} {
			if a[name] == "" {
				continue
			}
			n, err := strconv.Atoi(a[name])
			if err != nil {
				return nil, err
			}
			*into = n
		}
		p.provider.Script(answer)
		return map[string]string{}, nil
	})
	handle("release", func(map[string]string) (any, error) { p.provider.Release(); return map[string]string{}, nil })
	handle("usage", func(map[string]string) (any, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		records := make([]map[string]any, 0, len(p.usage))
		for _, u := range p.usage {
			records = append(records, map[string]any{
				"namespace": u.Bot.Namespace, "name": u.Bot.Name, "uid": u.Bot.UID, "model": u.Model,
				"status": u.Status, "inputTokens": u.InputTokens, "outputTokens": u.OutputTokens,
				"cacheCreationInputTokens": u.CacheCreationInputTokens, "cacheReadInputTokens": u.CacheReadInputTokens,
			})
		}
		return map[string]any{"usage": records}, nil
	})
	handle("restart", func(map[string]string) (any, error) { return map[string]string{}, p.start() })
	handle("clock", func(a map[string]string) (any, error) {
		advance, err := time.ParseDuration(a["advance"])
		if err != nil {
			return nil, err
		}
		p.offset.Add(int64(advance))
		return map[string]string{}, nil
	})
	handle("limits", func(map[string]string) (any, error) {
		return map[string]int{
			"botBurst": botBurst, "botEverySeconds": int(botEvery / time.Second),
			"issuerBurst": issuerBurst, "issuerEverySeconds": int(issuerEvery / time.Second),
		}, nil
	})
	return mux
}

// selfSigned makes the gateway's serving certificate for 127.0.0.1, and its PEM for the suite to trust.
func selfSigned() (tls.Certificate, []byte, error) {
	key, err := newKey()
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "model-gateway-harness"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	trust := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, trust, nil
}
