package conformance_test

import (
	"encoding/pem"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/api/agent/v1alpha1/agentfake"
	"github.com/garamsh/garam-bot/api/agent/v1alpha1/conformance"
	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
	"github.com/garamsh/garam-bot/api/model-gateway/v1alpha1/gatewayfake"
)

const token = "conformance-token-0123456789abcdef"

const (
	modelTokenFile = "/run/garam-bot/model/token"
	modelTrustFile = "/run/garam-bot/model/trust.pem"
)

// TestReference runs the suite against the reference adapter, served over HTTP, with interface 5's
// gatewayfake as its model gateway, served over TLS on a certificate the trust file holds.
func TestReference(t *testing.T) {
	model, bot := startGateway(t)
	gatewayServer := httptest.NewTLSServer(model)
	t.Cleanup(gatewayServer.Close)
	gatewayRoot := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gatewayServer.Certificate().Raw})
	// The workspace's model token and trust files, as the bot's process writes them and the harness
	// reads them.
	var (
		mu         sync.Mutex
		modelToken string
		modelTrust = gatewayRoot
	)
	files := func(path string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case path == modelTokenFile && modelToken != "":
			return []byte(modelToken + "\n"), nil
		case path == modelTrustFile:
			return modelTrust, nil
		}
		return nil, os.ErrNotExist
	}
	write := func(token string) error {
		mu.Lock()
		defer mu.Unlock()
		modelToken = token
		return nil
	}
	writeTrust := func(roots []byte) error {
		mu.Lock()
		defer mu.Unlock()
		modelTrust = roots
		return nil
	}
	issued, err := model.Issue(bot)
	if err != nil {
		t.Fatal(err)
	}
	if err := write(issued.Token); err != nil {
		t.Fatal(err)
	}
	adapter := agentfake.New(func() ([]byte, error) { return []byte(token + "\n"), nil }).WithModel(files, nil)
	server := httptest.NewServer(adapter)
	t.Cleanup(server.Close)
	conformance.Run(t, conformance.Subject{
		BaseURL:         server.URL,
		Client:          server.Client(),
		Token:           token,
		Gateway:         model,
		ModelBot:        bot,
		WriteModelToken: write,
		GatewayTrust:    gatewayRoot,
		WriteModelTrust: writeTrust,
		Session: v1alpha1.SessionSpec{
			Instructions: "You are the conformance suite's bot.",
			Model:        v1alpha1.Model{BaseURL: gatewayServer.URL, TokenFile: modelTokenFile, TrustFile: modelTrustFile},
			Tools:        []v1alpha1.Tool{{Name: "memory", URL: "https://tools.example/memory", TokenFile: "/run/garam-bot/tools/memory/token"}},
		},
	})
}

// startGateway serves gatewayfake in front of its stand-in provider, and answers the bot whose token
// the workspace holds.
func startGateway(t *testing.T) (*gatewayfake.Gateway, gateway.TokenRequest) {
	t.Helper()
	provider := httptest.NewServer(&gatewayfake.Provider{})
	t.Cleanup(provider.Close)
	upstream, err := url.Parse(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	signing, err := gatewayfake.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := gatewayfake.NewSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	model, err := gatewayfake.New(gatewayfake.Config{
		Upstream: upstream, Key: "sk-conformance-provider", SigningKey: signing,
		Credentials: &gatewayfake.Credentials{Key: credentials},
	})
	if err != nil {
		t.Fatal(err)
	}
	return model, gateway.TokenRequest{Namespace: "conformance", Name: "suite", UID: "uid-suite"}
}

// TestReferenceCaller runs the caller suite against the reference caller.
func TestReferenceCaller(t *testing.T) {
	conformance.RunCaller(t, conformance.ReferenceCaller{})
}
