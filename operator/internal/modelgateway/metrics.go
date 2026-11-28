package modelgateway

import (
	"regexp"
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// The bounds on what a metric's labels take. A value past its bound is counted as otherLabel, so no
// caller can grow a series without end.
const (
	// maxBotSeries bounds the bots counted apart, as the limiters bound the callers they hold.
	maxBotSeries = maxCallers
	// maxModelSeries bounds the models counted apart. A place's bots ask for a few.
	maxModelSeries = 32
	// otherLabel is a label value past its bound.
	otherLabel = "other"
	// invalidModel is a model name the gateway does not record as asked (boundedModel).
	invalidModel = "invalid"
)

// modelPattern is a model name the gateway records as asked: the provider's names are of these
// characters, and a name that is not one could carry what the call holds.
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,128}$`)

// boundedModel is model where it is a name of modelPattern, and invalidModel otherwise, so neither a
// record nor a label carries content of the call (USE-1, SEC-1).
func boundedModel(model string) string {
	if modelPattern.MatchString(model) {
		return model
	}
	return invalidModel
}

// Metrics counts each forwarded call and its tokens per bot, model and status (USE-1).
type Metrics struct {
	calls  *prometheus.CounterVec
	tokens *prometheus.CounterVec

	bots   *bounded
	models *bounded
}

// NewMetrics registers the gateway's counters with registry.
func NewMetrics(registry prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "garam_model_gateway_calls_total",
			Help: "Model calls forwarded to the provider, by the bot's namespace and name, the model and the upstream's status.",
		}, []string{"namespace", "bot", "model", "status"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "garam_model_gateway_tokens_total",
			Help: "Tokens the provider reported, by the bot's namespace and name, the model, and the kind: " +
				"input, output, cache_creation or cache_read.",
		}, []string{"namespace", "bot", "model", "kind"}),
		bots:   newBounded(maxBotSeries),
		models: newBounded(maxModelSeries),
	}
	for _, c := range []prometheus.Collector{m.calls, m.tokens} {
		if err := registry.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Record counts u.
func (m *Metrics) Record(u Usage) {
	namespace, bot := u.Bot.Namespace, u.Bot.Name
	if !m.bots.take(namespace + "/" + bot) {
		namespace, bot = otherLabel, otherLabel
	}
	model := u.Model
	if !m.models.take(model) {
		model = otherLabel
	}
	m.calls.WithLabelValues(namespace, bot, model, strconv.Itoa(u.Status)).Inc()
	for kind, n := range map[string]int64{
		"input": u.InputTokens, "output": u.OutputTokens,
		"cache_creation": u.CacheCreationInputTokens, "cache_read": u.CacheReadInputTokens,
	} {
		if n > 0 {
			m.tokens.WithLabelValues(namespace, bot, model, kind).Add(float64(n))
		}
	}
}

// bounded is a set that takes at most max values: a value is taken where it is held already, or
// while the set holds fewer than max.
type bounded struct {
	max int

	mu   sync.Mutex
	held map[string]struct{}
}

func newBounded(maxValues int) *bounded {
	return &bounded{max: maxValues, held: map[string]struct{}{}}
}

func (b *bounded) take(value string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.held[value]; ok {
		return true
	}
	if len(b.held) >= b.max {
		return false
	}
	b.held[value] = struct{}{}
	return true
}
