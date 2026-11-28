package workspaceaccess

import (
	"errors"
	"fmt"
	"time"

	access "github.com/garamsh/garam-bot/api/workspace-access/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// Bot names a Bot by its namespace and name, which are also its ServiceAccount's.
type Bot struct {
	Namespace string
	Name      string
}

func (b Bot) String() string { return b.Namespace + "/" + b.Name }

// Declared is a Bot as its namespace declares it now.
type Declared struct {
	UID     string
	Harness string
	// Attachments are its workspaces, by the attachment's name.
	Attachments map[string]Attachment
	// Tools are the names of the tools it may call.
	Tools []string
}

// Attachment is one workspace a Bot attaches.
type Attachment struct {
	// Class names the WorkspaceClass, in the Bot's namespace, its workspace is made from.
	Class string
	// Unsupported is why this release cannot serve the attachment, and empty where it can (WS-1).
	Unsupported string
}

// Class is a WorkspaceClass: the provider that makes its workspaces, and what each gets.
type Class struct {
	Provider  string
	Resources workspace.Resources
}

// Pod is a Pod as the API server holds it now.
type Pod struct {
	UID            string
	ServiceAccount string
	// BotUID is the UID of the Bot whose StatefulSet controls the Pod, and empty where no Bot's does.
	BotUID string
	// Container is the containerID of the Pod's runtime container while that container runs, and
	// empty otherwise: a Pod whose runtime container does not run is no running instance (CRED-2).
	Container string
}

// The refusals a call answers, each mapped to interface 7's status and code in one place (handler.go).
var (
	errCredential        = errors.New("the place credential is not accepted")
	errProof             = errors.New("the DPoP proof is not accepted")
	errKey               = errors.New("another key is pinned to the instance")
	errUnknownBot        = errors.New("the credential names no Bot this release serves")
	errNotFound          = errors.New("not found")
	errInvalid           = errors.New("invalid")
	errTooLarge          = errors.New("the content is above 64 KiB")
	errNotRunning        = errors.New("the workspace is not running")
	errDeleting          = errors.New("the workspace's earlier incarnation is being deleted")
	errUnsupported       = errors.New("unsupported")
	errWaitingForGateway = errors.New("the place has no issuer of session tokens")
	errServerDials       = errors.New("the server's dials are at their cap")
)

// limitedError is a limit reached, with how long to wait (LIMIT-1 to LIMIT-3).
type limitedError struct {
	what string
	wait time.Duration
}

func (e limitedError) Error() string { return e.what + " is reached" }

// fileError is a write that failed at a path, the files before it written (FILE-1).
type fileError struct {
	path string
	err  error
}

func (e fileError) Error() string { return fmt.Sprintf("the write failed at %s: %v", e.path, e.err) }
func (e fileError) Unwrap() error { return e.err }

// codes are the refusals' interface 7 statuses and codes, in the one place a domain error is
// translated.
var codes = []struct {
	err    error
	status int
	code   access.Code
}{
	{errCredential, 401, access.CodeInvalidCredential},
	{errProof, 401, access.CodeInvalidDPoPProof},
	{errKey, 401, access.CodeInvalidKey},
	{errUnknownBot, 403, access.CodeUnknownBot},
	{errNotFound, 404, access.CodeNotFound},
	{errInvalid, 400, access.CodeInvalid},
	{errTooLarge, 413, access.CodeInvalid},
	{errNotRunning, 409, access.CodeNotRunning},
	{errDeleting, 409, access.CodeDeleting},
	{errUnsupported, 422, access.CodeUnsupported},
	{errWaitingForGateway, 503, access.CodeWaitingForGateway},
	{errServerDials, 503, access.CodeTemporarilyUnavailable},
}
