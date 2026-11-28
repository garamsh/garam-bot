package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"

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
	cfg, err := configOf(validOptions(t, "  sk-organization-own\n"), slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	assert.Equal(t, "sk-organization-own", cfg.Key)
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
	} {
		t.Run(name, func(t *testing.T) {
			o := validOptions(t, "sk-organization-own")
			change(&o)
			_, err := configOf(o, slog.New(slog.DiscardHandler))
			assert.Error(t, err)
		})
	}
	_, err := configOf(validOptions(t, " \n"), slog.New(slog.DiscardHandler))
	assert.Error(t, err, "an empty key file is taken: a gateway with no key serves no model call")
}

func TestRecordUsage_LogsTheBotTheModelAndEachCount(t *testing.T) {
	var out bytes.Buffer
	record := recordUsage(slog.New(slog.NewJSONHandler(&out, nil)))
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
}
