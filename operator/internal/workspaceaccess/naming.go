package workspaceaccess

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"

	agent "github.com/garamsh/garam-bot/api/agent/v1alpha1"
	workspace "github.com/garamsh/garam-bot/api/workspace/v1alpha1"
)

// The labels each workspace carries where its provider keeps labels (ADR 0099 decision 5), which the
// deletion sweep reads back.
const (
	LabelReleaseNamespace = "bot.garam.sh/workspace-release-namespace"
	LabelReleaseName      = "bot.garam.sh/workspace-release-name"
	LabelBotNamespace     = "bot.garam.sh/bot-namespace"
	LabelBotName          = "bot.garam.sh/bot-name"
	LabelBotUID           = "bot.garam.sh/bot-uid"
	LabelAttachment       = "bot.garam.sh/attachment"
)

// Release is the Helm release a connector is installed by.
type Release struct {
	Namespace string
	Name      string
}

// Prefix is what begins the name of every workspace of the release: w and the first 12 hex digits of
// the SHA-256 of <namespace>/<name>, and a hyphen.
func (r Release) Prefix() string {
	return "w" + digest(r.Namespace + "/" + r.Name)[:12] + "-"
}

// workspaceName is the workspace of the Bot's attachment, by the Bot's identity: a Bot made again under
// its name gets a new one (WS-1).
func workspaceName(r Release, bot Bot, uid, attachment string) workspace.Name {
	return workspace.Name(r.Prefix() + digest(bot.Namespace + "/" + bot.Name + "/" + uid + "/" + attachment)[:40])
}

// labelsOf are the labels of the Bot's attachment's workspace.
func labelsOf(r Release, bot Bot, uid, attachment string) map[string]string {
	return map[string]string{
		LabelReleaseNamespace: r.Namespace, LabelReleaseName: r.Name,
		LabelBotNamespace: bot.Namespace, LabelBotName: bot.Name, LabelBotUID: uid, LabelAttachment: attachment,
	}
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// specOf is the interface 3 spec of a workspace of class for the Bot's harness at place: the harness's
// image, interface 2's port, and the egress the place gives that provider's workspaces (WS-1).
func specOf(image string, class Class, place Place) workspace.Spec {
	return workspace.Spec{
		Image:     image,
		Resources: class.Resources,
		Ports:     []uint16{agent.Port},
		Egress:    slices.Clone(place.Egress),
	}
}
