package modelgateway

import (
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gateway "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

func TestBoundedModel_TakesAProviderNameAlone(t *testing.T) {
	for _, name := range []string{"claude-sonnet-4-5", "claude-3-5-haiku@20241022", "anthropic.claude:v2"} {
		assert.Equal(t, name, boundedModel(name))
	}
	for _, other := range []string{"", "a model with spaces", "claude\nnext", strings.Repeat("m", 129), "모델"} {
		assert.Equal(t, invalidModel, boundedModel(other), "%q", other)
	}
}

func TestMetrics_CountNoMoreModelsOrBotsApartThanTheirBounds(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	require.NoError(t, err)
	for i := range maxModelSeries + 10 {
		metrics.Record(Usage{Bot: gateway.TokenRequest{Namespace: "ns", Name: "reviewer"}, Model: "model-" + strconv.Itoa(i), Status: 200})
	}
	series, err := testutil.GatherAndCount(registry, "garam_model_gateway_calls_total")
	require.NoError(t, err)
	assert.Equal(t, maxModelSeries+1, series, "the models past the bound are not counted as one")
	assert.Equal(t, 10.0, testutil.ToFloat64(metrics.calls.WithLabelValues("ns", "reviewer", otherLabel, "200")))

	bots := newBounded(2)
	assert.True(t, bots.take("a"))
	assert.True(t, bots.take("b"))
	assert.False(t, bots.take("c"), "a value past the bound is taken")
	assert.True(t, bots.take("a"), "a value held is refused once the bound is reached")
}
