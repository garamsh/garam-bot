package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"

	botkube "github.com/garamsh/garam-bot/operator/internal/bottoken/kube"
	"github.com/garamsh/garam-bot/operator/internal/modelgateway"
)

func validOptions(t *testing.T, key string) options {
	t.Helper()
	file := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(file, []byte(key), 0o600))
	return options{
		certFile: "tls.crt", keyFile: "tls.key", upstream: "https://api.anthropic.com", providerKeyFile: file,
		issuer: "garam-system/garam-connector", namespace: "garam-system",
	}
}

func TestConfigOf_ReadsTheProviderKeyOnceTrimmed(t *testing.T) {
	cfg, _, err := configOf(validOptions(t, "  sk-organization-own\n"), func(modelgateway.Usage) {})
	require.NoError(t, err)
	assert.Equal(t, "sk-organization-own", cfg.Key.Current())
	assert.Equal(t, "https://api.anthropic.com", cfg.Upstream.String())
	assert.Equal(t, "garam-system/garam-connector", cfg.Issuer)
}

func TestConfigOf_RefusesToStartWithoutWhatTheGatewayNeeds(t *testing.T) {
	for name, change := range map[string]func(*options){
		"no serving certificate": func(o *options) { o.certFile = "" },
		"no key file":            func(o *options) { o.providerKeyFile = "" },
		"a key file not there":   func(o *options) { o.providerKeyFile = filepath.Join(t.TempDir(), "absent") },
		"no issuer":              func(o *options) { o.issuer = "" },
		"no namespace":           func(o *options) { o.namespace = "" },
		"an http upstream":       func(o *options) { o.upstream = "http://api.anthropic.com" },
		"credentials in the url": func(o *options) { o.upstream = "https://user:secret@api.anthropic.com" },
		"a key of no header value": func(o *options) {
			require.NoError(t, os.WriteFile(o.providerKeyFile, []byte("sk-one\nsk-two"), 0o600))
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := validOptions(t, "sk-organization-own")
			change(&o)
			_, _, err := configOf(o, func(modelgateway.Usage) {})
			assert.Error(t, err)
		})
	}
	_, _, err := configOf(validOptions(t, " \n"), func(modelgateway.Usage) {})
	assert.Error(t, err, "an empty key file is taken: a gateway with no key serves no model call")
}

func TestRecordUsage_LogsAndCountsTheBotTheModelAndEachCount(t *testing.T) {
	var out bytes.Buffer
	registry := prometheus.NewRegistry()
	metrics, err := modelgateway.NewMetrics(registry)
	require.NoError(t, err)
	record := recordUsage(slog.New(slog.NewJSONHandler(&out, nil)), metrics)
	record(modelgateway.Usage{
		Bot: gateway.TokenRequest{Namespace: "ns", Name: "reviewer", UID: "4f1c"}, Model: "claude-test", Status: 200,
		InputTokens: 11, OutputTokens: 7, CacheCreationInputTokens: 3, CacheReadInputTokens: 5,
	})
	var line map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &line))
	for name, want := range map[string]any{
		"namespace": "ns", "bot": "reviewer", "uid": "4f1c", "model": "claude-test", "status": 200.0,
		"inputTokens": 11.0, "outputTokens": 7.0, "cacheCreationInputTokens": 3.0, "cacheReadInputTokens": 5.0,
	} {
		assert.Equal(t, want, line[name], "%s", name)
	}
	count, err := testutil.GatherAndCount(registry, "garam_model_gateway_calls_total", "garam_model_gateway_tokens_total")
	require.NoError(t, err)
	assert.Equal(t, 5, count, "the call is counted once, and each of its four kinds of tokens")
}

// TestAPIServerKeys_ReadsTheIssuerAndItsKeysFromTheAPIServersDiscovery runs the adapter over a client
// of an API server that answers the two documents interface 6 reads them from.
func TestAPIServerKeys_ReadsTheIssuerAndItsKeysFromTheAPIServersDiscovery(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k", Algorithm: "ES256", Use: "sig"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": "https://kubernetes.default.svc", "jwks_uri": "https://kubernetes.default.svc/openid/v1/jwks",
			})
		case "/openid/v1/jwks":
			_ = json.NewEncoder(w).Encode(set)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	require.NoError(t, err)
	read, err := apiServerKeys{botkube.Keys{Kube: kube}}.Keys(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "https://kubernetes.default.svc", read.URL)
	assert.NotEmpty(t, read.Keys.Key("k"), "the API server's key is not read")
}
