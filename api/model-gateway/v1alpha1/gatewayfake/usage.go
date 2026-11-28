package gatewayfake

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	v1alpha1 "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

// call is what the gateway knows of a forwarded call, carried to its answer.
type call struct {
	bot   v1alpha1.TokenRequest
	model string
}

type callKey struct{}

func withCall(ctx context.Context, c *call) context.Context {
	return context.WithValue(ctx, callKey{}, c)
}

func callOf(ctx context.Context) *call {
	c, _ := ctx.Value(callKey{}).(*call)
	return c
}

// counted are the tokens a provider's answer reports.
type counted struct{ input, output int64 }

// metered passes an answer's body through, reading the provider's usage from it, and records it once
// the body ends (USE-1).
type metered struct {
	body   io.ReadCloser
	stream bool
	done   func(counted)

	seen     bytes.Buffer
	recorded bool
}

func (m *metered) Read(p []byte) (int, error) {
	n, err := m.body.Read(p)
	if n > 0 && m.seen.Len() < 1<<20 {
		m.seen.Write(p[:n])
	}
	if err == io.EOF {
		m.record()
	}
	return n, err
}

func (m *metered) Close() error {
	m.record()
	return m.body.Close()
}

func (m *metered) record() {
	if m.recorded {
		return
	}
	m.recorded = true
	m.done(usageOf(m.seen.Bytes(), m.stream))
}

type usageBody struct {
	Usage   *usageCounts `json:"usage"`
	Message *struct {
		Usage *usageCounts `json:"usage"`
	} `json:"message"`
}

type usageCounts struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// usageOf reads the usage of a Messages answer, whole or as a stream of events.
func usageOf(body []byte, stream bool) counted {
	var c counted
	add := func(data []byte) {
		var b usageBody
		if json.Unmarshal(data, &b) != nil {
			return
		}
		for _, u := range []*usageCounts{b.Usage, messageUsage(b)} {
			if u != nil {
				c.input = max(c.input, u.InputTokens)
				c.output = max(c.output, u.OutputTokens)
			}
		}
	}
	if !stream {
		add(body)
		return c
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if data, ok := strings.CutPrefix(scanner.Text(), "data:"); ok {
			add([]byte(strings.TrimSpace(data)))
		}
	}
	return c
}

func messageUsage(b usageBody) *usageCounts {
	if b.Message == nil {
		return nil
	}
	return b.Message.Usage
}

func isStream(response *http.Response) bool {
	kind, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	return kind == "text/event-stream"
}
