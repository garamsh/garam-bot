package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
)

// taskID is the form TASK-1 gives a task's identifier, which an inbox file's name is.
var taskID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// MaxMessage is the most an inbox message may hold. A larger one is set aside unread.
const MaxMessage = 1 << 20

// rejectedSuffix ends the name a message no task could be made of is set aside under, after a ".",
// so it is skipped and kept for whoever wrote it.
const rejectedSuffix = ".rejected"

// Inbox is a MessageSource over a directory until G4: each regular file is one message, given in the
// order of the names, its name the message's identifier and its content the input. A name starting
// with "." is skipped, so a writer can rename a file into place, and so is a name that is no task
// identifier. Done removes a message's file, and sets a rejected one aside as .<name>.rejected. A file
// that cannot be removed is not given again while the process runs.
type Inbox struct {
	dir    string
	poll   time.Duration
	sleep  func(ctx context.Context, d time.Duration) error
	logger *slog.Logger

	mu       sync.Mutex
	finished map[string]bool
}

// NewInbox returns the Inbox of dir, read again every poll while it holds no message.
func NewInbox(dir string, poll time.Duration, sleepFor func(ctx context.Context, d time.Duration) error, logger *slog.Logger) *Inbox {
	if poll <= 0 {
		poll = time.Second
	}
	if sleepFor == nil {
		sleepFor = sleep
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Inbox{dir: dir, poll: poll, sleep: sleepFor, logger: logger, finished: map[string]bool{}}
}

// Next answers the first message by name, as os.ReadDir sorts them, reading the directory again every
// poll while it holds none.
func (b *Inbox) Next(ctx context.Context) (Message, error) {
	for {
		m, ok, err := b.first()
		if err != nil || ok {
			return m, err
		}
		if err := b.sleep(ctx, b.poll); err != nil {
			return Message{}, err
		}
	}
}

func (b *Inbox) first() (Message, bool, error) {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return Message{}, false, fmt.Errorf("read the inbox: %w", err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || name[0] == '.' || !taskID.MatchString(name) || b.finished[name] {
			continue
		}
		content, err := b.read(name)
		if errors.Is(err, errTooLarge) {
			b.logger.Warn("message larger than the inbox takes; set aside", "message", name, "limit", MaxMessage)
			b.setAside(name)
			continue
		}
		if err != nil {
			return Message{}, false, err
		}
		return Message{ID: name, Input: []agent.Input{{Text: string(content)}}}, true, nil
	}
	return Message{}, false, nil
}

var errTooLarge = errors.New("the message is larger than the inbox takes")

func (b *Inbox) read(name string) ([]byte, error) {
	file, err := os.Open(filepath.Join(b.dir, name))
	if err != nil {
		return nil, fmt.Errorf("read the message %s: %w", name, err)
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, MaxMessage+1))
	if err != nil {
		return nil, fmt.Errorf("read the message %s: %w", name, err)
	}
	if len(content) > MaxMessage {
		return nil, errTooLarge
	}
	return content, nil
}

// Done removes m's file, or sets it aside where it was rejected.
func (b *Inbox) Done(_ context.Context, m Message, outcome agent.Outcome) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if outcome == OutcomeRejected {
		b.setAside(m.ID)
		return nil
	}
	if err := os.Remove(filepath.Join(b.dir, m.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		b.finished[m.ID] = true
		return fmt.Errorf("remove the message %s; it is skipped while the process runs: %w", m.ID, err)
	}
	return nil
}

// setAside renames the message name to .<name>.rejected, or skips it while the process runs where it
// cannot. b.mu is held.
func (b *Inbox) setAside(name string) {
	if err := os.Rename(filepath.Join(b.dir, name), filepath.Join(b.dir, "."+name+rejectedSuffix)); err != nil {
		b.finished[name] = true
		b.logger.Error("message not set aside; it is skipped while the process runs", "message", name, "error", err)
	}
}
