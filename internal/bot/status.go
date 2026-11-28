package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The status contract's timing: the process rewrites its status at HeartbeatInterval, and a status
// older than Freshness is one whose process no longer runs its Bot.
const (
	HeartbeatInterval = 5 * time.Second
	Freshness         = 30 * time.Second
)

// State is what the process is doing, as its status file says.
type State string

const (
	// StateRunning is a process running its Bot.
	StateRunning State = "Running"
	// StateStopped is a process that has stopped running its Bot.
	StateStopped State = "Stopped"
)

// Status is the content of the status file.
type Status struct {
	State     State        `json:"state"`
	Heartbeat time.Time    `json:"heartbeat"`
	Bot       BotRef       `json:"bot"`
	Token     *TokenStatus `json:"token,omitempty"`
	// Workspace is absent for a bot with no workspace.
	Workspace *WorkspaceStatus `json:"workspace,omitempty"`
}

// BotRef names the Bot the process runs, at the generation it read.
type BotRef struct {
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	Generation int64  `json:"generation,omitempty"`
}

// TokenStatus is when the garam token held was obtained and when it expires, both absent before the
// first, and whether the latest attempt to obtain one failed.
type TokenStatus struct {
	ObtainedAt time.Time `json:"obtainedAt,omitzero"`
	ExpiresAt  time.Time `json:"expiresAt,omitzero"`
	Stale      bool      `json:"stale"`
}

// ErrNotRunning is returned by Probe for a process that is not running its Bot.
var ErrNotRunning = errors.New("the bot is not running")

// WriteStatus replaces the status file at path, by a rename, so a reader never sees a partial one.
func WriteStatus(path string, status Status) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()

		return fmt.Errorf("write status: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("write status: %w", err)
	}

	return nil
}

// Probe reads the status file at path and returns nil only while it says the process runs its Bot
// with a heartbeat no older than Freshness at now. Otherwise it returns an error wrapping
// ErrNotRunning that says why.
func Probe(path string, now time.Time) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: no status: %w", ErrNotRunning, err)
	}
	var status Status
	if err := json.Unmarshal(data, &status); err != nil {
		return fmt.Errorf("%w: status unreadable: %w", ErrNotRunning, err)
	}
	if status.State != StateRunning {
		return fmt.Errorf("%w: state is %q", ErrNotRunning, status.State)
	}
	if age := now.Sub(status.Heartbeat); age > Freshness {
		return fmt.Errorf("%w: last heartbeat %s ago, past %s", ErrNotRunning, age.Round(time.Second), Freshness)
	}

	return nil
}
