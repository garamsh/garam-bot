// Command garam-bot runs one Bot of interface 1 v1alpha1: "garam-bot run" reads the Bot from a file,
// obtains the bot's garam token from its place's connector and runs it, and "garam-bot probe" answers
// the status contract a container's probe asks (ADR 0033, ADR 0042). "garam-bot workspace" serves a
// workspace's gRPC data plane (files and exec), whose operations its subcommands call, and its MCP
// server, the in-workspace tools a harness may be given (ADR 0027, ADR 0028, ADR 0040).
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/garamsh/garam-bot/internal/bot"
	"github.com/garamsh/garam-bot/internal/workspace/shell"
)

func main() {
	os.Exit(run())
}

// run is the binary's whole behaviour, returned as its exit status.
func run() int {
	// The workspace's executor execs this binary as its resource-limit shim for every untrusted
	// child (ADR 0008). The verb short-circuits the command tree: the shim caps itself and execs
	// the child's command, so it never returns to run a subcommand.
	shell.ShimIfRequested()

	err := NewRootCommand().Execute()
	reportCommandError(err)

	return exitStatus(err)
}

// reportCommandError prints the error of a command that is not a workspace operation as one
// "Error: <message>" line on stderr, which is the only diagnosis a failed start has. An operation
// prints its own JSON failure object, so it is not printed twice.
func reportCommandError(err error) {
	var op *operationError
	if err == nil || errors.As(err, &op) {
		return
	}
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

// NewRootCommand builds the garam-bot command tree. It is the package's one export: main runs it,
// and the package's tests drive the binary through it.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "garam-bot",
		Short:         "Run one Bot from a file, and probe whether it runs",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newRunCommand(), newProbeCommand(), newWorkspaceCommand())

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

			// Not cobra's required flag: its refusal would exit before the termination message is
			// written, and a container started with no --bot-file would report no reason.
			var err error
			if options.BotFile == "" {
				err = fmt.Errorf("%w: --bot-file not given", bot.ErrBotInvalid)
			} else {
				err = bot.NewService(options).Run(ctx)
			}
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
	flags.StringVar(&options.ConnectorURL, "connector-url", "", "base URL of the place's connector, which answers the bot's garam token and serves its workspace; unset runs the bot with neither")
	flags.StringVar(&options.ConnectorTrustFile, "connector-trust-file", "", "file holding the certificates the connector is trusted by, read again as it changes")
	flags.StringVar(&options.CredentialFile, "credential-file", "", "file holding the place's credential naming the bot for its garam token, read again for each request; unset runs the bot with no garam identity")
	flags.StringVar(&options.WorkspaceCredentialFile, "workspace-credential-file", "", "file holding the place's credential naming the bot for its workspace, read again for each request; unset, or for a Bot with no workspace, runs the bot with no workspace")
	flags.StringVar(&options.InboxDir, "inbox-dir", "", "directory of message files the bot's workspace is given tasks from, one per file, its name the task's identifier and its content the input, deleted once the task is done; unset gives no task, and it excludes --mailbox-url")
	flags.StringVar(&options.MailboxURL, "mailbox-url", "", "base URL the bot dials garam's machine listener at, to long-poll its mailbox for its tasks; given with --mailbox-origin, --garam-root-file, --bot-grn and --credential-file")
	flags.StringVar(&options.MailboxOrigin, "mailbox-origin", "", "garam's machine origin, which the mailbox poll's proofs name and every message's signature is issued by")
	flags.StringVar(&options.GaramRootFile, "garam-root-file", "", "file holding the root garam's machine listener's certificate chains to, read again as it changes")
	flags.StringVar(&options.BotGRN, "bot-grn", "", "the bot's garam GRN, grn:<org>/iam/bot/<declaration id>: the mailbox polled and every message's audience")
	flags.StringVar(&options.StatusFile, "status-file", defaultStatusFile, "file the status is written to")
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
