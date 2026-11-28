package v1alpha1

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// The bounds the specification sets.
const (
	// MaxFiles is the most files one write carries (FILE-1).
	MaxFiles = 16
	// MaxContentBytes is the most content one write carries, in all (FILE-1).
	MaxContentBytes = 64 << 10
	// MaxDialsPerInstance is the most dials a workload instance holds open at once (DIAL-3).
	MaxDialsPerInstance = 4
	// DialIdle is how long a dial may pass no byte before the server closes it (DIAL-3).
	DialIdle = 120 * time.Second
	// ProofWindow is how far a proof's iat may be from the server's clock (PROOF-1).
	ProofWindow = 60 * time.Second
	// ProofMemory is how long a server remembers a proof's jti (PROOF-1).
	ProofMemory = 2 * ProofWindow
	// MaxCredentialLife bounds a credential's exp - iat (CRED-2).
	MaxCredentialLife = time.Hour
	// MaxTrustBytes is the most a model's trust holds (TRUST-1).
	MaxTrustBytes = 16 << 10
	// TrustOverlap is how long a root that replaces another is answered beside it before the gateway
	// serves a chain ending in it (TRUST-1).
	TrustOverlap = time.Hour
)

// Paths below PathPrefix, each under the attachment's workspace (T-2).
const (
	// WorkspacesPath is followed by the attachment's name.
	WorkspacesPath = PathPrefix + "workspaces/"
	FilesSuffix    = "/files"
	DialSuffix     = "/dial"
	TokensSuffix   = "/session-tokens"
)

// HeaderAuthenticate carries the DPoP challenge of every 401 (PROOF-1).
const HeaderAuthenticate = "WWW-Authenticate"

// Workspace is the answer of an acquire and a read (WS-1, WS-2).
type Workspace struct {
	Attachment string          `json:"attachment"`
	Phase      workspace.Phase `json:"phase"`
	Reason     string          `json:"reason,omitempty"`
	Model      Model           `json:"model"`
	Tools      []Tool          `json:"tools"`
}

// Model is the model gateway the workspace reaches.
type Model struct {
	BaseURL string `json:"baseUrl"`
	// Trust is the PEM of the roots the gateway's certificate chains to, one or more CERTIFICATE
	// blocks, where the workspace's system roots do not verify it (TRUST-1). Empty, they do. Added
	// in release 0.3.0.
	Trust string `json:"trust,omitempty"`
}

// Tool is one MCP server the workspace reaches, at the place's tool proxy.
type Tool struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Files is the body of a write (FILE-1).
type Files struct {
	Files []File `json:"files"`
}

// File is one file of a write. Content is base64 in JSON.
type File struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// SessionTokens is the answer of a session tokens call (TOK-1).
type SessionTokens struct {
	Model Token `json:"model"`
	// Tools is keyed by each tool's name of the Workspace answer.
	Tools map[string]Token `json:"tools"`
}

// Token is one short-lived token and the seconds it lives.
type Token struct {
	Token     string `json:"token"`
	ExpiresIn int64  `json:"expiresIn"`
}

// ErrorBody is every refusal's body (T-3).
type ErrorBody struct {
	Error       Code   `json:"error"`
	Description string `json:"error_description,omitempty"`
	// Path is the file a write failed at, the files before it written (FILE-1).
	Path string `json:"path,omitempty"`
}

// Codes is every code of Code, for a reader that checks an answer names one of them.
var Codes = []Code{
	CodeInvalid, CodeInvalidCredential, CodeInvalidDPoPProof, CodeInvalidKey, CodeUnknownBot, CodeNotFound,
	CodeNotRunning, CodeDeleting, CodeUnsupported, CodeWaitingForGateway, CodeTemporarilyUnavailable,
}

// CheckTokenPath refuses a path FILE-1 does not take: one that is not clean, not under TokenDir, or
// TokenDir itself.
func CheckTokenPath(p string) error {
	if path.Clean(p) != p || !strings.HasPrefix(p, TokenDir) || len(p) <= len(TokenDir) {
		return fmt.Errorf("path %q is not a clean path under %s", p, TokenDir)
	}
	return nil
}

// CheckTrust refuses a model's trust TRUST-1 does not take: one above MaxTrustBytes, or one that is
// not one or more PEM CERTIFICATE blocks, each an X.509 certificate, with nothing else but space. An
// empty trust is taken.
func CheckTrust(trust string) error {
	if len(trust) > MaxTrustBytes {
		return fmt.Errorf("the trust holds more than %d bytes", MaxTrustBytes)
	}
	rest := []byte(trust)
	for n := 0; ; n++ {
		rest = bytes.TrimSpace(rest)
		if len(rest) == 0 {
			return nil
		}
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return errors.New("the trust holds something that is not a PEM block")
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("block %d of the trust is a %s, not a CERTIFICATE", n+1, block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("block %d of the trust is no X.509 certificate: %w", n+1, err)
		}
	}
}
