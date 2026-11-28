package gatewayfake

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"sync"

	v1alpha1 "github.com/garamsh/garam-bot/api/model-gateway/v1alpha1"
)

// Bounds on what an answer's usage is read from. A whole answer past maxWhole, or a stream event past
// maxEvent, passes on unread.
const (
	maxWhole = 16 << 20
	maxEvent = 1 << 20
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

// counted are the tokens an answer reports, each the last count reported of it (USE-1).
type counted struct {
	input, output, cacheCreation, cacheRead int64
}

type usageCounts struct {
	InputTokens   *int64 `json:"input_tokens"`
	OutputTokens  *int64 `json:"output_tokens"`
	CacheCreation *int64 `json:"cache_creation_input_tokens"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
}

func (c *counted) add(data []byte) {
	var b struct {
		Usage   *usageCounts `json:"usage"`
		Message *struct {
			Usage *usageCounts `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &b) != nil {
		return
	}
	if b.Message != nil {
		c.take(b.Message.Usage)
	}
	c.take(b.Usage)
}

func (c *counted) take(u *usageCounts) {
	if u == nil {
		return
	}
	for _, f := range []struct {
		from *int64
		into *int64
	}{{u.InputTokens, &c.input}, {u.OutputTokens, &c.output}, {u.CacheCreation, &c.cacheCreation}, {u.CacheRead, &c.cacheRead}} {
		if f.from != nil {
			*f.into = *f.from
		}
	}
}

// metered passes an answer's body through unchanged, reading the provider's usage as it passes, and
// records the call once, when the body ends or is closed (USE-1). A stream is read event by event, so
// its usage is read however long it runs.
type metered struct {
	body   io.ReadCloser
	stream bool
	done   func(counted)

	counted counted
	pending bytes.Buffer
	over    bool
	once    sync.Once
}

func (m *metered) Read(p []byte) (int, error) {
	n, err := m.body.Read(p)
	if n > 0 {
		m.see(p[:n])
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

func (m *metered) see(b []byte) {
	if !m.stream {
		if m.pending.Len()+len(b) > maxWhole {
			m.over = true
		}
		if !m.over {
			m.pending.Write(b)
		}
		return
	}
	for len(b) > 0 {
		line, rest, found := bytes.Cut(b, []byte("\n"))
		switch {
		case m.over:
			// The rest of a line past maxEvent is skipped to its end.
			m.over = !found
		case m.pending.Len()+len(line) > maxEvent:
			m.pending.Reset()
			m.over = !found
		default:
			m.pending.Write(line)
			if found {
				if data, ok := bytes.CutPrefix(bytes.TrimRight(m.pending.Bytes(), "\r"), []byte("data:")); ok {
					m.counted.add(bytes.TrimSpace(data))
				}
				m.pending.Reset()
			}
		}
		if !found {
			return
		}
		b = rest
	}
}

func (m *metered) record() {
	m.once.Do(func() {
		if !m.stream && !m.over {
			m.counted.add(m.pending.Bytes())
		}
		m.pending.Reset()
		m.done(m.counted)
	})
}

func isStream(response *http.Response) bool {
	kind, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	return kind == "text/event-stream"
}
