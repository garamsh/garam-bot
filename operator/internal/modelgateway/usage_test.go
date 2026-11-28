package modelgateway

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// read passes body through a metered reader in chunks of size, as a proxy copies it, and answers what
// it recorded and how often.
func read(t *testing.T, body string, stream bool, size int) (counted, int, string) {
	t.Helper()
	var got counted
	calls := 0
	m := &metered{body: io.NopCloser(strings.NewReader(body)), stream: stream, done: func(c counted) { got, calls = c, calls+1 }}
	var out bytes.Buffer
	chunk := make([]byte, size)
	for {
		n, err := m.Read(chunk)
		out.Write(chunk[:n])
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	require.NoError(t, m.Close())
	return got, calls, out.String()
}

func TestMetered_ReadsAStreamsLastCountsPastAnEventItSkips(t *testing.T) {
	stream := "event: message_start\ndata: {\"message\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":1,\"cache_read_input_tokens\":5}}}\n\n" +
		// Long enough to span chunks after it passes maxEvent, so its end is skipped too.
		"data: " + strings.Repeat("x", maxEvent+3*4096) + "\n\n" +
		"event: message_delta\r\ndata: {\"usage\":{\"output_tokens\":9999}}\r\n\r\n"
	got, calls, out := read(t, stream, true, 4096)
	assert.Equal(t, stream, out, "the stream is not passed on unchanged")
	assert.Equal(t, 1, calls, "the call is recorded more than once, or never")
	assert.Equal(t, counted{input: 11, output: 9999, cacheRead: 5}, got)
}

func TestMetered_RecordsAWholeAnswerPastItsBoundWithoutItsCounts(t *testing.T) {
	small := `{"usage":{"input_tokens":3,"output_tokens":4}}`
	got, calls, _ := read(t, small, false, 7)
	require.Equal(t, 1, calls)
	assert.Equal(t, counted{input: 3, output: 4}, got, "control: a whole answer within the bound is read")
	large := `{"content":"` + strings.Repeat("x", maxWhole) + `","usage":{"input_tokens":3,"output_tokens":4}}`
	got, calls, out := read(t, large, false, 64<<10)
	assert.Equal(t, large, out)
	assert.Equal(t, 1, calls, "a whole answer past the bound is not recorded")
	assert.Equal(t, counted{}, got)
}

func TestMetered_RecordsOnceWhenTheBodyIsClosedBeforeItEnds(t *testing.T) {
	calls := 0
	m := &metered{body: io.NopCloser(strings.NewReader("data: {}\n")), stream: true, done: func(counted) { calls++ }}
	require.NoError(t, m.Close())
	require.NoError(t, m.Close())
	assert.Equal(t, 1, calls)
}
