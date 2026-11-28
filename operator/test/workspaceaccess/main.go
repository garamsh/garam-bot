//go:build e2e

// Command workspaceaccess serves the connector's interface 7, internal/workspaceaccess, over stand-ins
// for the API server, the watched namespaces and a provider, and is the fixture interface 7's server
// suite drives it by (api/workspace-access/v1alpha1/conformance.md). It is built for that run alone,
// under the build tag e2e, and no release carries it.
//
//	workspaceaccess --dir DIR serve
//	workspaceaccess --dir DIR <fixture subcommand> [arguments]
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
	"encoding/base64"
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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
	"github.com/garamsh/garam-bot/api/workspace/v1alpha1/workspacefake"
	"github.com/garamsh/garam-bot/operator/internal/workspaceaccess"
)

// The files serve and the fixture subcommands share in the directory.
const (
	urlFile     = "url"
	trustFile   = "ca.pem"
	controlFile = "control"
	keyFile     = "issuer-key.pem"
	startedFile = "started"
)

const (
	issuerURL = "https://kubernetes.default.svc.cluster.local"
	keyID     = "harness"
	harness   = "claude-code"
	image     = "registry.invalid/workspace@sha256:" + "0000000000000000000000000000000000000000000000000000000000000000"
	// namespaces is how many conformance-<n> namespaces the release watches, as the suite names one per
	// bot it places.
	namespaces = 400
	// provider and class are the one provider the place runs and the one class its Bots name.
	podProvider = "pod"
	// linkCommand is the subcommand that plants a link.
	linkCommand = "link"
	class       = "standard"
)

// The fixture's answers (conformance.md §The fixture).
type (
	botAnswer struct {
		Namespace string `json:"namespace,omitempty"`
		Name      string `json:"name,omitempty"`
		UID       string `json:"uid"`
	}
	instanceAnswer struct {
		Instance string `json:"instance"`
	}
	fileAnswer struct {
		Exists  bool   `json:"exists"`
		Content string `json:"content,omitempty"`
		Link    string `json:"link,omitempty"`
	}
	limitsAnswer struct {
		BotBurst             int `json:"botBurst"`
		BotEverySeconds      int `json:"botEverySeconds"`
		InstanceBurst        int `json:"instanceBurst"`
		InstanceEverySeconds int `json:"instanceEverySeconds"`
	}
	existsAnswer struct {
		Exists bool `json:"exists"`
	}
	maxDialsAnswer struct {
		Previous int `json:"previous"`
	}
)

func main() {
	dir := flag.String("dir", "", "the directory serve and the fixture subcommands share")
	flag.Parse()
	if *dir == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: workspaceaccess --dir DIR serve | <fixture subcommand> [arguments]")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	var answer []byte
	switch name := flag.Arg(0); name {
	case "serve":
		err = serve(ctx, *dir)
	case "credential":
		answer, err = credential(*dir, flag.Args()[1:])
	default:
		answer, err = forward(ctx, *dir, flag.Args())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, _ = os.Stdout.Write(answer)
}

func serve(ctx context.Context, dir string) error {
	issuerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(issuerKey)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	serving, certificate, err := servingCertificate()
	if err != nil {
		return err
	}
	baseURL := "https://" + listener.Addr().String()
	started := time.Now().Truncate(time.Second)
	for name, content := range map[string][]byte{
		urlFile:     []byte(baseURL + "\n"),
		trustFile:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}),
		controlFile: []byte("http://" + controlListener.Addr().String() + "\n"),
		keyFile:     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}),
		startedFile: []byte(strconv.FormatInt(started.Unix(), 10) + "\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return err
		}
	}
	p := newPlace(&issuerKey.PublicKey)
	watched := make([]string, 0, namespaces+1)
	for n := range namespaces + 1 {
		watched = append(watched, fmt.Sprintf("conformance-%d", n))
	}
	service, err := workspaceaccess.NewService(workspaceaccess.Config{
		Release:    workspaceaccess.Release{Namespace: "garam-system", Name: "garam"},
		Namespaces: watched, Origins: []string{baseURL},
		Keys: p, Pods: p, Bots: p, LiveBots: p,
		Places: map[string]workspaceaccess.Place{podProvider: {
			Provider: p.provider, ModelURL: "https://gateway.harness.invalid/v1", ToolProxyURL: "https://tools.harness.invalid",
		}},
		Images: map[string]string{harness: image}, Issuer: p,
		Now: p.now, BindingFresh: time.Millisecond, Recheck: 100 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	p.service = service
	server := &http.Server{
		Handler: service.Handler(), ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{serving}, MinVersion: tls.VersionTLS13},
	}
	control := &http.Server{Handler: p.control(), ReadHeaderTimeout: 10 * time.Second}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	failed := make(chan error, 3)
	go func() { failed <- service.Run(ctx) }()
	go func() { failed <- server.ServeTLS(listener, "", "") }()
	go func() { failed <- control.Serve(controlListener) }()
	fmt.Println(`{"ready":true}`)
	select {
	case <-ctx.Done():
	case err = <-failed:
	}
	_ = server.Close()
	_ = control.Close()
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// place is what the connector is served over: the API server's keys and Pods, the watched namespaces'
// Bots, a provider, a clock and an issuer, each changed by the fixture.
type place struct {
	key      *ecdsa.PublicKey
	provider *provider
	service  workspaceaccess.Service

	mu     sync.Mutex
	offset time.Duration
	bots   map[workspaceaccess.Bot]*bot
	pods   map[string]*pod // by instance
	issuer bool
}

type bot struct {
	uid         string
	attachments map[string]string // attachment → why it is unsupported, empty where it is served
	tools       []string
}

type pod struct {
	namespace, name, uid, serviceAccount, botUID, container string
}

func newPlace(key *ecdsa.PublicKey) *place {
	return &place{
		key: key, provider: newProvider(),
		bots: map[workspaceaccess.Bot]*bot{}, pods: map[string]*pod{}, issuer: true,
	}
}

func (p *place) now() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Now().Add(p.offset)
}

func (p *place) Keys(context.Context) (workspaceaccess.Issuer, error) {
	return workspaceaccess.Issuer{URL: issuerURL, Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: p.key, KeyID: keyID, Algorithm: string(jose.ES256), Use: "sig"},
	}}}, nil
}

func (p *place) Pod(_ context.Context, namespace, name string) (workspaceaccess.Pod, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, held := range p.pods {
		if held.namespace == namespace && held.name == name {
			return workspaceaccess.Pod{
				UID: held.uid, ServiceAccount: held.serviceAccount, BotUID: held.botUID, Container: held.container,
			}, true, nil
		}
	}
	return workspaceaccess.Pod{}, false, nil
}

func (p *place) Served(context.Context, string) (bool, error) { return true, nil }

func (p *place) Bot(_ context.Context, b workspaceaccess.Bot) (workspaceaccess.Declared, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	held, ok := p.bots[b]
	if !ok {
		return workspaceaccess.Declared{}, false, nil
	}
	declared := workspaceaccess.Declared{
		UID: held.uid, Harness: harness, Attachments: map[string]workspaceaccess.Attachment{}, Tools: held.tools,
	}
	for name, unsupported := range held.attachments {
		declared.Attachments[name] = workspaceaccess.Attachment{Class: class, Unsupported: unsupported}
	}
	return declared, true, nil
}

// UID reads a Bot as the API server holds it now: the fixture's Bots are the API server's.
func (p *place) UID(_ context.Context, b workspaceaccess.Bot) (string, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	held, ok := p.bots[b]
	if !ok {
		return "", false, nil
	}
	return held.uid, true, nil
}

func (p *place) Class(_ context.Context, _, name string) (workspaceaccess.Class, bool, error) {
	if name != class {
		return workspaceaccess.Class{}, false, nil
	}
	resources := workspace.Resources{CPU: 1, MemoryBytes: 1 << 30, DiskBytes: 1 << 30}
	return workspaceaccess.Class{Provider: podProvider, Resources: resources}, true, nil
}

func (p *place) Available(context.Context) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.issuer
}

func (p *place) Tokens(_ context.Context, _ workspaceaccess.Bot, tools []string) (access.SessionTokens, error) {
	answer := access.SessionTokens{
		Model: access.Token{Token: "harness_model_" + rand.Text(), ExpiresIn: 3600}, Tools: map[string]access.Token{},
	}
	for _, tool := range tools {
		answer.Tools[tool] = access.Token{Token: "harness_tool_" + rand.Text(), ExpiresIn: 3600}
	}
	return answer, nil
}

func newID() string { return strings.ToLower(rand.Text()[:12]) }

// control serves the fixture's subcommands on a loopback listener no protocol client is given.
func (p *place) control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fixture", func(w http.ResponseWriter, r *http.Request) {
		var args []string
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args) == 0 {
			http.Error(w, "the body is the subcommand and its arguments", http.StatusBadRequest)
			return
		}
		answer, err := p.run(r.Context(), args[0], args[1:])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	})
	return mux
}

func (p *place) run(ctx context.Context, name string, args []string) (any, error) {
	switch name {
	case "bot":
		return p.addBot(args)
	case "remake", "remove":
		if len(args) != 2 {
			return nil, fmt.Errorf("%s takes NAMESPACE NAME", name)
		}
		answer, err := p.change(name, workspaceaccess.Bot{Namespace: args[0], Name: args[1]})
		if err != nil {
			return nil, err
		}
		// The earlier Bot's workspaces are deleted, past the sweep's grace, before this returns.
		workspaceaccess.SweepPastGrace(ctx, p.service)
		return answer, nil
	case "instance":
		return p.addInstance(args)
	case "restart", "end":
		if len(args) != 1 {
			return nil, fmt.Errorf("%s takes INSTANCE", name)
		}
		id, _, _ := strings.Cut(args[0], ".")
		p.mu.Lock()
		defer p.mu.Unlock()
		held, ok := p.pods[id]
		if !ok {
			return nil, errors.New("no such instance")
		}
		if name == "end" {
			delete(p.pods, id)
		} else {
			held.container = "containerd://" + newID()
		}
		return instanceAnswer{Instance: args[0]}, nil
	case "workspace":
		return p.provider.move(ctx, args)
	case "seed":
		return p.provider.seed(ctx, args)
	case "file", linkCommand:
		return p.provider.reach(ctx, name, args)
	case "exists":
		if len(args) != 2 {
			return nil, errors.New("exists takes UID ATTACHMENT")
		}
		_, err := p.provider.named(ctx, args[0], args[1])
		return existsAnswer{Exists: err == nil}, nil
	case "issuer":
		if len(args) != 1 || (args[0] != "--on" && args[0] != "--off") {
			return nil, errors.New("issuer takes --on or --off")
		}
		p.mu.Lock()
		p.issuer = args[0] == "--on"
		p.mu.Unlock()
		return struct{}{}, nil
	case "max-dials":
		n, err := strconv.Atoi(strings.Join(args, ""))
		if err != nil || n < 0 || len(args) != 1 {
			return nil, errors.New("max-dials takes N")
		}
		return maxDialsAnswer{Previous: workspaceaccess.SetMaxDials(p.service, n)}, nil
	case "clock":
		flags := flag.NewFlagSet("clock", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		advance := flags.Duration("advance", 0, "")
		if err := flags.Parse(args); err != nil {
			return nil, errors.New("clock takes --advance DURATION")
		}
		p.mu.Lock()
		p.offset += *advance
		p.mu.Unlock()
		return struct{}{}, nil
	case "limits":
		bot, botEvery, inst, instEvery := workspaceaccess.Limits()
		return limitsAnswer{
			BotBurst: bot, BotEverySeconds: botEvery, InstanceBurst: inst, InstanceEverySeconds: instEvery,
		}, nil
	}
	return nil, fmt.Errorf("unknown subcommand %q", name)
}

// change removes the Bot, or makes it again under its name with a new UID.
func (p *place) change(name string, key workspaceaccess.Bot) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	held, ok := p.bots[key]
	if !ok {
		return nil, errors.New("no such bot")
	}
	if name == "remove" {
		delete(p.bots, key)
		return struct{}{}, nil
	}
	made := *held
	made.uid = newID()
	p.bots[key] = &made
	return botAnswer{UID: made.uid}, nil
}

func (p *place) addBot(args []string) (any, error) {
	if len(args) < 2 {
		return nil, errors.New("bot takes NAMESPACE NAME and its flags")
	}
	key := workspaceaccess.Bot{Namespace: args[0], Name: args[1]}
	held := &bot{uid: newID(), attachments: map[string]string{}}
	for i := 2; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--attachment":
			held.attachments[args[i+1]] = ""
		case "--unsupported":
			held.attachments[args[i+1]] = "the harness's fixture declared it unsupported"
		case "--tool":
			held.tools = append(held.tools, args[i+1])
		default:
			return nil, fmt.Errorf("bot takes no %s", args[i])
		}
	}
	p.mu.Lock()
	p.bots[key] = held
	p.mu.Unlock()
	return botAnswer{Namespace: key.Namespace, Name: key.Name, UID: held.uid}, nil
}

func (p *place) addInstance(args []string) (any, error) {
	held := &pod{name: "pod-" + newID(), uid: newID(), container: "containerd://" + newID()}
	switch {
	case len(args) == 1 && args[0] == "--no-bot":
		held.namespace, held.serviceAccount = "conformance-0", "stranger"
	case len(args) == 2:
		held.namespace, held.serviceAccount = args[0], args[1]
		p.mu.Lock()
		if b, ok := p.bots[workspaceaccess.Bot{Namespace: args[0], Name: args[1]}]; ok {
			held.botUID = b.uid
		}
		p.mu.Unlock()
	default:
		return nil, errors.New("instance takes NAMESPACE NAME, or --no-bot")
	}
	id := newID()
	p.mu.Lock()
	p.pods[id] = held
	p.mu.Unlock()
	// The credential subcommand runs in another process, so the instance's Pod travels in its name.
	named := []string{id, held.namespace, held.serviceAccount, held.name, held.uid}
	return instanceAnswer{Instance: strings.Join(named, ".")}, nil
}

// credential signs a projected ServiceAccount token naming the instance's Pod, as the API server does.
func credential(dir string, args []string) ([]byte, error) {
	flags := flag.NewFlagSet("credential", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	audience := flags.String("audience", workspaceaccess.DefaultAudience, "")
	expired := flags.Bool("expired", false, "")
	lifetime := flags.Duration("lifetime", 10*time.Minute, "")
	beforeStart := flags.Bool("before-start", false, "")
	noIAT := flags.Bool("no-iat", false, "")
	var positional []string
	for rest := args; ; {
		if err := flags.Parse(rest); err != nil {
			return nil, err
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	if len(positional) != 1 {
		return nil, errors.New("credential takes INSTANCE and its flags")
	}
	parts := strings.Split(positional[0], ".")
	if len(parts) != 5 {
		return nil, errors.New("no such instance")
	}
	namespace, account, podName, podUID := parts[1], parts[2], parts[3], parts[4]
	read, err := os.ReadFile(filepath.Join(dir, keyFile))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(read)
	if block == nil {
		return nil, errors.New("no issuer key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	startedAt, err := os.ReadFile(filepath.Join(dir, startedFile))
	if err != nil {
		return nil, err
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(startedAt)), 10, 64)
	if err != nil {
		return nil, err
	}
	started := time.Unix(seconds, 0)
	now := time.Now()
	expiry := now.Add(*lifetime)
	switch {
	case *expired:
		time.Sleep(time.Until(started.Add(2*time.Second + 100*time.Millisecond)))
		now, expiry = started, time.Now().Add(-time.Second)
	case *beforeStart:
		now, expiry = started.Add(-time.Second), time.Now().Add(10*time.Minute)
	}
	options := (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, options)
	if err != nil {
		return nil, err
	}
	claims := jwt.Claims{
		Issuer: issuerURL, Subject: "system:serviceaccount:" + namespace + ":" + account, Audience: jwt.Audience{*audience},
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(expiry),
	}
	if *noIAT {
		claims.IssuedAt = nil
	}
	private := map[string]any{"kubernetes.io": map[string]any{
		"namespace": namespace, "pod": map[string]string{"name": podName, "uid": podUID},
		"serviceaccount": map[string]string{"name": account, "uid": "sa-" + podUID},
	}}
	raw, err := jwt.Signed(signer).Claims(claims).Claims(private).Serialize()
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"credential": raw})
}

// forward runs a subcommand on the running server's control listener.
func forward(ctx context.Context, dir string, args []string) ([]byte, error) {
	read, err := os.ReadFile(filepath.Join(dir, controlFile))
	if err != nil {
		return nil, fmt.Errorf("no server runs from %s: %w", dir, err)
	}
	body, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	at := strings.TrimSpace(string(read)) + "/fixture"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, at, bytes.NewReader(body))
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
		return nil, errors.New(strings.TrimSpace(string(answer)))
	}
	return answer, nil
}

func servingCertificate() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: "workspaceaccess harness"},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, der, nil
}

// provider is workspacefake with what a cluster's workspace does beside it: a phase the fixture sets,
// an incarnation still deleting, and links planted in it, which a write follows none of (interface 3's
// FILE-3).
type provider struct {
	*workspacefake.Provider

	mu       sync.Mutex
	phases   map[workspace.Name]workspace.Phase
	deleting map[workspace.Name]bool
	links    map[workspace.Name]map[string]string
}

func newProvider() *provider {
	return &provider{
		Provider: workspacefake.New(workspace.Capabilities{Snapshot: true, Egress: true, Stdin: true, Labels: true}),
		phases:   map[workspace.Name]workspace.Phase{}, deleting: map[workspace.Name]bool{},
		links: map[workspace.Name]map[string]string{},
	}
}

func (p *provider) Create(
	ctx context.Context, name workspace.Name, spec workspace.Spec,
) (workspace.Workspace, bool, error) {
	p.mu.Lock()
	deleting := p.deleting[name]
	p.mu.Unlock()
	if deleting {
		return workspace.Workspace{}, false, fmt.Errorf("%w: %s", workspace.ErrDeleting, name)
	}
	ws, created, err := p.Provider.Create(ctx, name, spec)
	return p.phased(ws), created, err
}

func (p *provider) Get(ctx context.Context, name workspace.Name) (workspace.Workspace, error) {
	ws, err := p.Provider.Get(ctx, name)
	return p.phased(ws), err
}

func (p *provider) Stop(ctx context.Context, name workspace.Name, grace time.Duration) (workspace.Workspace, error) {
	p.mu.Lock()
	delete(p.phases, name)
	p.mu.Unlock()
	return p.Provider.Stop(ctx, name, grace)
}

func (p *provider) WriteFile(ctx context.Context, name workspace.Name, path string, content []byte) error {
	p.mu.Lock()
	links := p.links[name]
	for i := 1; i < len(path); i++ {
		if path[i] == '/' && links[path[:i]] != "" {
			p.mu.Unlock()
			return fmt.Errorf("%w: %s passes through a link", workspace.ErrInvalid, path)
		}
	}
	delete(links, path)
	p.mu.Unlock()
	return p.Provider.WriteFile(ctx, name, path, content)
}

// phased is ws with the phase the fixture set, while it set one.
func (p *provider) phased(ws workspace.Workspace) workspace.Workspace {
	p.mu.Lock()
	defer p.mu.Unlock()
	if phase, ok := p.phases[ws.Name]; ok {
		ws.Status.Phase = phase
		ws.Status.Reason = "the harness's fixture set it"
	}
	if p.deleting[ws.Name] {
		ws.Status.Phase = workspace.PhaseStopping
	}
	return ws
}

// named answers the workspace of the Bot's attachment, by the labels the connector gives it.
func (p *provider) named(ctx context.Context, uid, attachment string) (workspace.Name, error) {
	all, err := p.List(ctx)
	if err != nil {
		return "", err
	}
	for _, ws := range all {
		labels := ws.Spec.Labels
		if labels[workspaceaccess.LabelBotUID] == uid && labels[workspaceaccess.LabelAttachment] == attachment {
			return ws.Name, nil
		}
	}
	return "", errors.New("no such workspace")
}

func (p *provider) move(ctx context.Context, args []string) (any, error) {
	if len(args) != 3 {
		return nil, errors.New("workspace takes UID ATTACHMENT --stopped|--failed|--pending|--deleting|--running")
	}
	name, err := p.named(ctx, args[0], args[1])
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	delete(p.phases, name)
	delete(p.deleting, name)
	p.mu.Unlock()
	switch args[2] {
	case "--stopped":
		_, err = p.Provider.Stop(ctx, name, 0)
	case "--running":
		_, err = p.Start(ctx, name)
	case "--failed", "--pending":
		p.mu.Lock()
		p.phases[name] = workspace.Phase(strings.TrimPrefix(args[2], "--"))
		p.mu.Unlock()
	case "--deleting":
		p.mu.Lock()
		p.deleting[name] = true
		p.mu.Unlock()
	default:
		return nil, errors.New("workspace takes --stopped, --failed, --pending, --deleting or --running")
	}
	return struct{}{}, err
}

// seed writes a file on the workspace's data volume, as the bot's work there does.
func (p *provider) seed(ctx context.Context, args []string) (any, error) {
	if len(args) != 4 {
		return nil, errors.New("seed takes UID ATTACHMENT PATH CONTENT")
	}
	if !strings.HasPrefix(args[2], workspace.DataPath+"/") {
		return nil, fmt.Errorf("seed writes under %s/ alone", workspace.DataPath)
	}
	name, err := p.named(ctx, args[0], args[1])
	if err != nil {
		return nil, err
	}
	return struct{}{}, p.Provider.WriteFile(ctx, name, args[2], []byte(args[3]))
}

func (p *provider) reach(ctx context.Context, name string, args []string) (any, error) {
	want := map[string]int{"file": 3, linkCommand: 4}[name]
	if len(args) != want {
		return nil, fmt.Errorf("%s takes UID ATTACHMENT and its paths", name)
	}
	ws, err := p.named(ctx, args[0], args[1])
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.links[ws] == nil {
		p.links[ws] = map[string]string{}
	}
	if name == linkCommand {
		p.links[ws][args[2]] = args[3]
		return fileAnswer{Exists: true, Link: args[3]}, nil
	}
	if target := p.links[ws][args[2]]; target != "" {
		return fileAnswer{Exists: true, Link: target}, nil
	}
	content, err := p.ReadFile(ctx, ws, args[2])
	if errors.Is(err, workspace.ErrFileNotFound) {
		return fileAnswer{}, nil
	}
	if err != nil {
		return nil, err
	}
	return fileAnswer{Exists: true, Content: base64.StdEncoding.EncodeToString(content)}, nil
}
