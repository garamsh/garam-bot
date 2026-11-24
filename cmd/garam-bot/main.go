// Command garam-bot runs one Bot of interface 1 v1alpha1: "garam-bot run" reads the Bot and the
// bot's garam token from files and runs it, and "garam-bot probe" answers the status contract a
// container's probe asks (ADR 0033).
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/garamsh/sherlock/internal/bot"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "garam-bot",
		Short:         "Run one Bot from a file, and probe whether it runs",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRunCommand(), newProbeCommand())

	return root
}

// defaultStatusFile is where run writes the status and probe reads it, on a volume both share.
const defaultStatusFile = "/run/garam-bot/status.json"

func newRunCommand() *cobra.Command {
	var options bot.Options
	var terminationLog string
	command := &cobra.Command{
		Use:   "run",
		Short: "Run the one Bot in --bot-file until SIGTERM",
		RunE: func(command *cobra.Command, _ []string) error {
			logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			options.Logger = logger
			options.Now = time.Now
			options.HeartbeatInterval = bot.HeartbeatInterval
			ctx, stop := signal.NotifyContext(command.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()

			err := bot.NewService(options).Run(ctx)
			if err == nil {
				return nil
			}
			logger.Error("bot not started", "reason", bot.ReasonOf(err), "error", err)
			// The kubelet reports this file as the container's termination message.
			message := fmt.Sprintf("%s: %v", bot.ReasonOf(err), err)
			if writeErr := os.WriteFile(terminationLog, []byte(message), 0o644); writeErr != nil {
				logger.Error("termination message not written", "path", terminationLog, "error", writeErr)
			}

			return err
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.BotFile, "bot-file", "", "file holding the one Bot to run (required)")
	flags.StringVar(&options.TokenFile, "token-file", "", "file holding the bot's garam token; unset runs the bot with no garam identity")
	flags.StringVar(&options.StatusFile, "status-file", defaultStatusFile, "file the status is written to")
	flags.DurationVar(&options.TokenInterval, "token-interval", 10*time.Second, "how often the token file is read again")
	flags.StringVar(&terminationLog, "termination-log", "/dev/termination-log", "file a failed start writes its reason to")

	return command
}

func newProbeCommand() *cobra.Command {
	var statusFile string
	command := &cobra.Command{
		Use:   "probe",
		Short: "Exit 0 while the bot runs, by the status file run writes",
		RunE: func(command *cobra.Command, _ []string) error {
			if err := bot.Probe(statusFile, time.Now()); err != nil {
				fmt.Fprintln(command.OutOrStdout(), err)

				return err
			}

			return nil
		},
	}
	command.Flags().StringVar(&statusFile, "status-file", defaultStatusFile, "file run writes the status to")

	return command
}
