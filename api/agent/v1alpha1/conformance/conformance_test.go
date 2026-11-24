package conformance_test

import (
	"net/http/httptest"
	"testing"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/api/agent/v1alpha1/agentfake"
	"github.com/garamsh/garam-bot/api/agent/v1alpha1/conformance"
)

const token = "conformance-token-0123456789abcdef"

// TestReference runs the suite against the reference adapter, served over HTTP.
func TestReference(t *testing.T) {
	server := httptest.NewServer(agentfake.New(func() ([]byte, error) { return []byte(token + "\n"), nil }))
	t.Cleanup(server.Close)
	conformance.Run(t, conformance.Subject{
		BaseURL: server.URL,
		Client:  server.Client(),
		Token:   token,
		Session: v1alpha1.SessionSpec{
			Instructions: "You are the conformance suite's bot.",
			Model:        v1alpha1.Model{BaseURL: "https://gateway.example", TokenFile: "/run/garam-bot/model/token"},
			Tools:        []v1alpha1.Tool{{Name: "memory", URL: "https://tools.example/memory", TokenFile: "/run/garam-bot/tools/memory/token"}},
		},
	})
}
