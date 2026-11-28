// Package bot runs one Bot of interface 1 v1alpha1 as a process: it holds the Bot read from a file
// and the bot's garam token obtained from its place's connector, and reports in a status file that it
// runs.
package bot

import "errors"

// ErrBotInvalid is returned when the Bot file is absent, unreadable or not one admissible Bot.
var ErrBotInvalid = errors.New("bot file invalid")

// Reason names why a start failed, as the status contract reports it.
type Reason string

const (
	// ReasonBotInvalid is a start that failed on ErrBotInvalid.
	ReasonBotInvalid Reason = "BotInvalid"
	// ReasonFailed is any other failed start.
	ReasonFailed Reason = "Failed"
)

// ReasonOf returns the reason a start that failed with err reports.
func ReasonOf(err error) Reason {
	switch {
	case errors.Is(err, ErrBotInvalid):
		return ReasonBotInvalid
	default:
		return ReasonFailed
	}
}
