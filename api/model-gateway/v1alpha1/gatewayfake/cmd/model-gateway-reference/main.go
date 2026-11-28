// Command model-gateway-reference serves interface 5's reference gateway, gatewayfake, for the server
// suite (conformance.md), and is the suite's fixture: `serve` runs the gateway, a stand-in provider
// behind it and a control listener, and every other subcommand asks that listener.
package main

import (
	"bytes"
	"crypto/ecdsa"
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

	v1alpha1 "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
	"github.com/garamsh/garam-bot/api/model-gateway/v1alpha1/gatewayfake"
)

// The files serve writes in its directory for the suite and the fixture.
const (
	trustFile   = "ca.pem"
	urlFile     = "url"
	controlFile = "control"
)

// The reference's choices, which `limits` and `credential` print (conformance.md §The fixture).
const (
	issuer = "reference-issuer"
	// Each bot's limit is small, so LIMIT-1 is reached in a few calls; the issuer's is room for every
	// case's tokens.
	botBurst    = 5
	botEvery    = 2 * time.Second
	issuerBurst = 50
	issuerEvery = time.Second
	providerKey = "sk-reference-provider-key-0123456789"
)

const usage = `usage: model-gateway-reference --dir DIR <subcommand> [arguments]

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
	flags := flag.NewFlagSet("model-gateway-reference", flag.ContinueOnError)
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
	switch command {
	case "credential":
		sub.Func("issuer", "", func(v string) error { body["issuer"] = v; return nil })
		sub.Func("audience", "", func(v string) error { body["audience"] = v; return nil })
		sub.Func("lifetime", "", func(v string) error { body["lifetime"] = v; return nil })
		sub.BoolFunc("expired", "", func(string) error { body["expired"] = "true"; return nil })
		sub.BoolFunc("foreign", "", func(string) error { body["foreign"] = "true"; return nil })
	case "token":
		for _, name := range []string{"namespace", "name", "uid"} {
			sub.Func(name, "", func(v string) error { body[name] = v; return nil })
		}
		sub.BoolFunc("foreign", "", func(string) error { body["foreign"] = "true"; return nil })
	case "seen":
		sub.BoolFunc("redirected", "", func(string) error { body["redirected"] = "true"; return nil })
	case "answer":
		for _, name := range []string{"status", "retry-after", "redirect"} {
			sub.Func(name, "", func(v string) error { body[name] = v; return nil })
		}
		for _, name := range []string{"stream", "long", "break", "close"} {
			sub.BoolFunc(name, "", func(string) error { body[name] = "true"; return nil })
		}
	case "clock":
		sub.Func("advance", "", func(v string) error { body["advance"] = v; return nil })
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

// place is what serve runs: the gateway, restartable, its provider, and its clock.
type place struct {
	basePath    string
	provider    *gatewayfake.Provider
	elsewhere   *gatewayfake.Recorder
	upstream    *url.URL
	credentials *gatewayfake.Credentials
	signing     *ecdsa.PrivateKey
	// foreign is a gateway of another key, and foreignCredentials a place's of another key, whose
	// tokens and credentials the gateway does not trust (MOD-1, ISS-1).
	foreign            *gatewayfake.Gateway
	foreignCredentials *gatewayfake.Credentials
	offset             atomic.Int64
	mu                 sync.Mutex
	gateway            *gatewayfake.Gateway
}

func (p *place) now() time.Time { return time.Now().Add(time.Duration(p.offset.Load())) }

func (p *place) start() error {
	g, err := gatewayfake.New(gatewayfake.Config{
		BasePath: p.basePath, Upstream: p.upstream, Key: providerKey, SigningKey: p.signing,
		Credentials: p.credentials, Issuers: []string{issuer}, Now: p.now,
		BotBurst: botBurst, BotEvery: botEvery, IssuerBurst: issuerBurst, IssuerEvery: issuerEvery,
	})
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.gateway = g
	p.mu.Unlock()
	return nil
}

// others makes the foreign gateway and credentials, each of a key of its own.
func (p *place) others() error {
	key, err := gatewayfake.NewSigningKey()
	if err != nil {
		return err
	}
	credentialKey, err := gatewayfake.NewSigningKey()
	if err != nil {
		return err
	}
	p.foreignCredentials = &gatewayfake.Credentials{Key: credentialKey}
	p.foreign, err = gatewayfake.New(gatewayfake.Config{
		Upstream: p.upstream, Key: providerKey, SigningKey: key, Credentials: p.foreignCredentials, Now: p.now,
	})
	return err
}

func (p *place) current() *gatewayfake.Gateway {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gateway
}

func serve(dir, basePath string) error {
	p := &place{basePath: basePath, provider: &gatewayfake.Provider{}, elsewhere: &gatewayfake.Recorder{}}
	elsewhere := httptest.NewServer(p.elsewhere)
	defer elsewhere.Close()
	p.provider.Elsewhere = elsewhere.URL
	upstream := httptest.NewServer(p.provider)
	defer upstream.Close()
	p.upstream, _ = url.Parse(upstream.URL)
	credentialKey, err := gatewayfake.NewSigningKey()
	if err != nil {
		return err
	}
	p.credentials = &gatewayfake.Credentials{Key: credentialKey}
	// The signing key outlives a restart, as a Secret the gateway writes does (TOKEN-2).
	if p.signing, err = gatewayfake.NewSigningKey(); err != nil {
		return err
	}
	if err := p.start(); err != nil {
		return err
	}
	if err := p.others(); err != nil {
		return err
	}
	certificate, trust, err := selfSigned()
	if err != nil {
		return err
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
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
		trustFile: trust, urlFile: []byte("https://" + listener.Addr().String() + basePath), controlFile: []byte("http://" + control.Addr().String()),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return err
		}
	}
	fmt.Println("ready")
	select {}
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
		name, o := issuer, gatewayfake.CredentialOptions{Audience: a["audience"]}
		if a["issuer"] != "" {
			name = a["issuer"]
		}
		if a["lifetime"] != "" {
			life, err := time.ParseDuration(a["lifetime"])
			if err != nil {
				return nil, err
			}
			o.Life = life
		}
		if a["expired"] != "" {
			o.IssuedAt, o.Life = p.now().Add(-20*time.Minute), 10*time.Minute
		}
		credentials := p.credentials
		if a["foreign"] != "" {
			credentials = p.foreignCredentials
		}
		credential, err := credentials.Issue(name, p.now(), o)
		return map[string]string{"credential": credential, "issuer": name}, err
	})
	handle("token", func(a map[string]string) (any, error) {
		g := p.current()
		if a["foreign"] != "" {
			g = p.foreign
		}
		return g.Issue(v1alpha1.TokenRequest{Namespace: a["namespace"], Name: a["name"], UID: a["uid"]})
	})
	handle("provider-key", func(map[string]string) (any, error) { return map[string]string{"key": providerKey}, nil })
	handle("seen", func(a map[string]string) (any, error) {
		if a["redirected"] != "" {
			return map[string]any{"seen": p.elsewhere.Seen()}, nil
		}
		return map[string]any{"seen": p.provider.Seen()}, nil
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
		records := []map[string]any{}
		for _, u := range p.current().Usage() {
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
	key, err := gatewayfake.NewSigningKey()
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "model-gateway-reference"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
