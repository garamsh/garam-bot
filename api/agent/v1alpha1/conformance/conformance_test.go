package conformance_test

import (
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

const modelTokenFile = "/run/garam-bot/model/token"

// TestReference runs the suite against the reference adapter, served over HTTP, with interface 5's
// gatewayfake as its model gateway.
func TestReference(t *testing.T) {
	model, bot := startGateway(t)
	gatewayServer := httptest.NewServer(model)
	t.Cleanup(gatewayServer.Close)
	// The workspace's model token file, as the bot's process writes it and the harness reads it.
	var (
		mu         sync.Mutex
		modelToken string
	)
	files := func(path string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if path != modelTokenFile || modelToken == "" {
			return nil, os.ErrNotExist
		}
		return []byte(modelToken + "\n"), nil
	}
	write := func(token string) error {
		mu.Lock()
		defer mu.Unlock()
		modelToken = token
		return nil
	}
	issued, err := model.Issue(bot)
	if err != nil {
		t.Fatal(err)
	}
	if err := write(issued.Token); err != nil {
		t.Fatal(err)
	}
	adapter := agentfake.New(func() ([]byte, error) { return []byte(token + "\n"), nil }).WithModel(files, gatewayServer.Client())
	server := httptest.NewServer(adapter)
	t.Cleanup(server.Close)
	conformance.Run(t, conformance.Subject{
		BaseURL:         server.URL,
		Client:          server.Client(),
		Token:           token,
		Gateway:         model,
		ModelBot:        bot,
		WriteModelToken: write,
		Session: v1alpha1.SessionSpec{
			Instructions: "You are the conformance suite's bot.",
			Model:        v1alpha1.Model{BaseURL: gatewayServer.URL, TokenFile: modelTokenFile},
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
