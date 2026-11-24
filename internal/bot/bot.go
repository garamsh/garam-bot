// Package bot runs one Bot of interface 1 v1alpha1 as a process: it holds the Bot read from a file
// and the bot's garam token read from another, and reports in a status file that it runs.
package bot

import "errors"

var (
	// ErrBotInvalid is returned when the Bot file is absent, unreadable or not one admissible Bot.
	ErrBotInvalid = errors.New("bot file invalid")
	// ErrTokenUnreadable is returned when the token file cannot be read or holds no token.
	ErrTokenUnreadable = errors.New("token file unreadable")
)

// Reason names why a start failed, as the status contract reports it.
type Reason string

const (
	// ReasonBotInvalid is a start that failed on ErrBotInvalid.
	ReasonBotInvalid Reason = "BotInvalid"
	// ReasonTokenUnreadable is a start that failed on ErrTokenUnreadable.
	ReasonTokenUnreadable Reason = "TokenUnreadable"
	// ReasonFailed is any other failed start.
	ReasonFailed Reason = "Failed"
)

// ReasonOf returns the reason a start that failed with err reports.
func ReasonOf(err error) Reason {
	switch {
	case errors.Is(err, ErrBotInvalid):
		return ReasonBotInvalid
	case errors.Is(err, ErrTokenUnreadable):
		return ReasonTokenUnreadable
	default:
		return ReasonFailed
	}
}
