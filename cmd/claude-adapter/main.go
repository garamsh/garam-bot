// Command claude-adapter serves the agent interface, v1alpha1 (api/agent/v1alpha1), in a workspace
// by running the Claude Code CLI. It listens on the interface's port and reads the bearer token
// from the interface's token file at each request.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/garamsh/garam-bot/api/agent/v1alpha1"
	"github.com/garamsh/garam-bot/internal/claudeadapter"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := run(ctx, log); err != nil {
		log.Error("the adapter stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	command := os.Getenv("CLAUDE_COMMAND")
	if command == "" {
		command = "claude"
	}
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", v1alpha1.Port),
		Handler:           claudeadapter.New(claudeadapter.Config{Command: command, Version: version(ctx, command), Log: log}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	return nil
}

// version asks the harness for its version, which is the first word of what `claude --version`
// prints, and says "unknown" when it cannot.
func version(ctx context.Context, command string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "--version").Output()
	if err != nil {
		return "unknown"
	}
	if f := strings.Fields(string(out)); len(f) > 0 {
		return f[0]
	}

	return "unknown"
}
