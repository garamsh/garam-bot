package conformance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

// ReferenceCaller is the smallest caller that keeps interface 2's obligations on one: the suite's
// own subject, as agentfake is the adapter's. A bot's driver binds the same cases to its own code
// with RunCaller, and does not copy this.
type ReferenceCaller struct{}

// ReadEvents reads one page of the session's events and keeps the types it knows (VER-2).
func (ReferenceCaller) ReadEvents(ctx context.Context, c Conn, session string, after uint64) ([]v1alpha1.Event, uint64, error) {
	var page struct {
		Events []json.RawMessage `json:"events"`
		Next   uint64            `json:"next"`
	}
	if err := get(ctx, c, "sessions/"+session+"/events?wait=0&after="+strconv.FormatUint(after, 10), &page); err != nil {
		return nil, after, err
	}
	var kept []v1alpha1.Event
	for _, raw := range page.Events {
		var e v1alpha1.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, after, fmt.Errorf("decode an event: %w", err)
		}
		switch e.Type {
		case v1alpha1.EventMessage, v1alpha1.EventToolCall, v1alpha1.EventToolResult, v1alpha1.EventTaskDone:
			kept = append(kept, e)
		}
	}

	return kept, page.Next, nil
}

// ReadState reads the session's state, and any state it does not know as unknown (VER-2).
func (ReferenceCaller) ReadState(ctx context.Context, c Conn, session string) (v1alpha1.SessionState, error) {
	var s v1alpha1.Session
	if err := get(ctx, c, "sessions/"+session, &s); err != nil {
		return "", err
	}
	switch s.State {
	case v1alpha1.SessionIdle, v1alpha1.SessionWorking:
		return s.State, nil
	default:
		return v1alpha1.SessionUnknown, nil
	}
}

func get(ctx context.Context, c Conn, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.BaseURL, "/")+v1alpha1.PathPrefix+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, body)
	}

	return json.Unmarshal(body, into)
}

// referenceLife is how long the reference caller lets a token live: half the hour CRED-1 allows,
// so one late Keep does not break it.
const referenceLife = 30 * time.Minute

// Tokens writes the adapter's token, the model's and each tool's, and Keep replaces each when it
// is half an hour old. Every token is 128 bits from the system's CSPRNG: the reference has no
// issuer to ask, and a bot's driver asks the gateway and the tool proxies for theirs.
func (ReferenceCaller) Tokens(ctx context.Context, files Files, spec v1alpha1.SessionSpec, place Place) (TokenKeeper, error) {
	paths := []string{v1alpha1.TokenPath, spec.Model.TokenFile}
	for _, tool := range spec.Tools {
		paths = append(paths, tool.TokenFile)
	}
	k := &referenceKeeper{files: files, paths: paths, place: place, written: map[string]time.Time{}}

	return k, k.Keep(ctx)
}

type referenceKeeper struct {
	files   Files
	paths   []string
	place   Place
	written map[string]time.Time
}

func (k *referenceKeeper) Keep(ctx context.Context) error {
	now := k.place.Now()
	for _, path := range k.paths {
		if at, ok := k.written[path]; ok && now.Sub(at) < referenceLife {
			continue
		}
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		if err := k.files.WriteFile(ctx, path, []byte(hex.EncodeToString(b))); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		k.written[path] = now
	}

	return nil
}
