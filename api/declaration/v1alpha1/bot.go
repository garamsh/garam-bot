package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Bot is one bot: who it is, the agent harness it works with, the workspaces it attaches and what
// it is permitted to do.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories=garam
// +kubebuilder:printcolumn:name="Harness",type=string,JSONPath=`.spec.agent.harness`
// +kubebuilder:printcolumn:name="Placement",type=string,JSONPath=`.spec.placement`
// +kubebuilder:printcolumn:name="Running",type=string,JSONPath=`.status.conditions[?(@.type=="Running")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:metadata:annotations="bot.garam.sh/spec-release=0.1.0"
type Bot struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec BotSpec `json:"spec"`
	// +optional
	Status BotStatus `json:"status,omitzero"`
}

// BotSpec is the declared state of a Bot.
//
// +kubebuilder:validation:XValidation:rule="!has(self.pool) || !has(self.placement) || self.placement != 'Dedicated'",message="a Dedicated bot names no pool"
type BotSpec struct {
	Persona Persona `json:"persona"`
	Agent   Agent   `json:"agent"`

	// Workspaces are the workspaces the bot works in, each under a name unique within the bot.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Workspaces []WorkspaceAttachment `json:"workspaces,omitempty"`

	// +optional
	Permissions Permissions `json:"permissions,omitzero"`

	// Placement is whether the bot shares a pod with other bots of its namespace or has one of its
	// own.
	// +optional
	// +kubebuilder:default=Shared
	Placement Placement `json:"placement,omitempty"`

	// Pool names the BotPool, in the bot's namespace, whose pods a Shared bot runs in.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Pool string `json:"pool,omitempty"`
}

// Persona is who the bot is.
type Persona struct {
	// Role is the bot's role in its organization, in a few words.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Role string `json:"role"`

	// Instructions are the bot's standing instructions, as text.
	// +optional
	// +kubebuilder:validation:MaxLength=65536
	Instructions string `json:"instructions,omitempty"`
}

// Agent is the harness the bot works with.
type Agent struct {
	// Harness names an implementation of garam-bot's agent interface, such as claude-code or codex.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Harness string `json:"harness"`
}

// WorkspaceAttachment attaches one workspace to the bot, either an existing Workspace by reference
// or a new one made from a template.
//
// +kubebuilder:validation:XValidation:rule="has(self.ref) != has(self.template)",message="exactly one of ref and template is set"
type WorkspaceAttachment struct {
	// Name is the attachment's name within the bot.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// +optional
	Ref *WorkspaceRef `json:"ref,omitempty"`

	// +optional
	Template *WorkspaceTemplate `json:"template,omitempty"`
}

// WorkspaceRef attaches an existing Workspace in the bot's namespace.
type WorkspaceRef struct {
	// Name is the Workspace's name.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name"`

	// +optional
	// +kubebuilder:default=Exclusive
	Access Access `json:"access,omitempty"`
}

// WorkspaceTemplate makes a workspace for the bot alone from a WorkspaceClass.
type WorkspaceTemplate struct {
	// Class names the WorkspaceClass, in the bot's namespace, the workspace is made from.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Class string `json:"class"`

	Lifetime Lifetime `json:"lifetime"`
}

// Access is how a bot holds a Workspace it attaches by reference.
// +kubebuilder:validation:Enum=Exclusive;SharedRead
type Access string

const (
	// AccessExclusive is the one bot that reads and writes the Workspace.
	AccessExclusive Access = "Exclusive"
	// AccessSharedRead is one of any number of bots that only read the Workspace.
	AccessSharedRead Access = "SharedRead"
)

// Lifetime is how long a workspace made from a template lives.
// +kubebuilder:validation:Enum=Session;Bot
type Lifetime string

const (
	// LifetimeSession makes a new workspace for each session and deletes it when the session ends.
	LifetimeSession Lifetime = "Session"
	// LifetimeBot keeps one workspace for as long as the Bot exists.
	LifetimeBot Lifetime = "Bot"
)

// Placement is where the bot's process runs.
// +kubebuilder:validation:Enum=Shared;Dedicated
type Placement string

const (
	// PlacementShared runs the bot in a pod with other bots of its namespace.
	PlacementShared Placement = "Shared"
	// PlacementDedicated runs the bot in a pod of its own.
	PlacementDedicated Placement = "Dedicated"
)

// Permissions are what the bot may do, enforced where the bot acts.
type Permissions struct {
	// Tools are the tools the bot may call, each named once.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Tools []ToolPermission `json:"tools,omitempty"`

	// Secrets names the Secret keys the bot may use, in the bot's namespace.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	Secrets []SecretKeyRef `json:"secrets,omitempty"`

	// Egress are the hosts the bot may reach, each named once.
	// +optional
	// +listType=map
	// +listMapKey=host
	// +kubebuilder:validation:MaxItems=64
	Egress []EgressPermission `json:"egress,omitempty"`
}

// ToolPermission is one tool the bot may call.
type ToolPermission struct {
	// Name is the tool's name.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// EgressPermission is one host the bot may reach.
type EgressPermission struct {
	// Host is a DNS name whose last label begins with a letter, so it is never an IP address.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)*[a-z]([-a-z0-9]*[a-z0-9])?$`
	Host string `json:"host"`
}

// ConditionRunning is the Bot condition that is True while the bot's process runs.
const ConditionRunning = "Running"

// BotStatus is the observed state of a Bot, written by what runs it.
type BotStatus struct {
	// ObservedGeneration is the generation of the spec the status describes.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BotList is a list of Bots.
//
// +kubebuilder:object:root=true
type BotList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bot `json:"items"`
}
