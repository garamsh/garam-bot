package bottoken

import (
	"errors"
	"fmt"
	"strings"
)

// Path is the one call the server answers, POST with no body (interface 6's T-2).
const Path = "/bot-token/v1alpha1/token"

// Bot names a Bot by its namespace and name, which are also its ServiceAccount's.
type Bot struct {
	Namespace string
	Name      string
}

func (b Bot) String() string { return b.Namespace + "/" + b.Name }

// GRN is a bot's garam GRN, grn:<org>/iam/bot/<declaration id>.
type GRN string

// BotGRN is the GRN of the bot declared by id in the organization of the cluster record.
func BotGRN(record, id string) (GRN, error) {
	rest, ok := strings.CutPrefix(record, "grn:")
	org, kind, found := strings.Cut(rest, "/")
	if !ok || !found || org == "" || !strings.HasPrefix(kind, "iam/cluster/") {
		return "", fmt.Errorf("the record %q is not a cluster GRN", record)
	}
	return GRN("grn:" + org + "/iam/bot/" + id), nil
}

// The refusals a call answers, each mapped to interface 6's code in one place (handler.go).
var (
	// errCredential is a credential that is missing, does not verify, has expired, names another
	// audience, or was spent (CRED-2, CRED-3).
	errCredential = errors.New("the place credential is not accepted")
	// errUnknownBot is a credential naming no Bot this record applied (BOT-1).
	errUnknownBot = errors.New("no garam-owned Bot of this record holds that name")
	// errRefused is an exchange garam refused for the bot (BOT-2).
	errRefused = errors.New("garam refused the exchange for the bot")
	// errUnavailable is garam unanswered, answering 5xx, or the actor token not held (BOT-2).
	errUnavailable = errors.New("garam's exchange is unavailable")
)

// The codes the server answers that garam answers too (interface 6's T-3).
const (
	codeUseNonce     = "use_dpop_nonce"
	codeInvalidProof = "invalid_dpop_proof"
)

// codeUnavailable is interface 6's answer to a request to try again (T-3).
const codeUnavailable = "temporarily_unavailable"

// proofError is garam's refusal of the proof, use_dpop_nonce or invalid_dpop_proof, relayed as it came.
type proofError struct {
	code string
}

func (e proofError) Error() string { return "garam refused the proof: " + e.code }

// limitedError is garam's 429, with the seconds it asked to wait.
type limitedError struct {
	retryAfter string
}

func (e limitedError) Error() string { return "garam limited the exchanges" }
