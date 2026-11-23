package conformance

import "strings"

// Case is one object the suite admits or refuses.
type Case struct {
	// Requirement is the identifier of the requirement in ../README.md the case tests.
	Requirement string
	// Name says what the case holds.
	Name string
	// Manifest is the object, as a YAML document.
	Manifest string
	// Admit is whether the spec admits the object.
	Admit bool
	// WantSpec, when set, is the admitted spec as JSON.
	WantSpec string
	// Pending, when set, says why the case cannot run yet; the case is skipped.
	Pending string
}

const header = "apiVersion: bot.garam.sh/v1alpha1\n"

// bot returns a Bot named mira whose spec is the minimal one with extra appended, extra being YAML
// lines indented under spec.
func bot(extra string) string {
	return header + `kind: Bot
metadata: {name: mira}
spec:
  persona: {role: Game Lead}
  agent: {harness: claude-code}
` + extra
}

// secrets returns a Bot whose permissions name the one secret reference ref.
func secrets(ref string) string {
	return bot("  permissions: {secrets: [\"" + ref + "\"]}\n")
}

// workspaceClass returns a WorkspaceClass named small whose spec is spec, a YAML flow mapping.
func workspaceClass(spec string) string {
	return header + "kind: WorkspaceClass\nmetadata: {name: small}\nspec: " + spec + "\n"
}

// Cases returns every case of the suite.
func Cases() []Case {
	return []Case{
		{Requirement: "SCH-1", Name: "a minimal Bot is admitted", Manifest: bot(""), Admit: true},
		{Requirement: "SCH-1", Name: "a Bot with every field is admitted", Admit: true, Manifest: strings.Replace(bot(`  workspaces:
  - {name: main, ref: {name: neocity-main, access: SharedRead}}
  - {name: scratch, template: {class: small, lifetime: Session}}
  permissions:
    tools: [git, shell]
    secrets: [deploy-key/ssh]
    egress: [github.com]
  placement: Dedicated
  pool: game-team
`), "{role: Game Lead}", `{role: Game Lead, instructions: "Lead the game team."}`, 1)},
		{Requirement: "SCH-1", Name: "a Workspace is admitted", Admit: true,
			Manifest: header + "kind: Workspace\nmetadata: {name: neocity-main}\nspec: {class: small}\n"},
		{Requirement: "SCH-1", Name: "a WorkspaceClass is admitted", Admit: true,
			Manifest: workspaceClass("{provider: mvmd, resources: {cpu: 2, memory: 4Gi, disk: 20Gi}, idle: {sleepAfter: 15m}}")},
		{Requirement: "SCH-1", Name: "a BotPool is admitted", Admit: true,
			Manifest: header + "kind: BotPool\nmetadata: {name: game-team}\nspec: {maxBotsPerPod: 50}\n"},

		{Requirement: "SCH-2", Name: "another version of the group is refused",
			Manifest: strings.Replace(bot(""), "v1alpha1", "v1", 1)},
		{Requirement: "SCH-2", Name: "a kind the spec does not declare is refused",
			Manifest: strings.Replace(bot(""), "kind: Bot", "kind: Agent", 1)},

		{Requirement: "SCH-3", Name: "an unknown field in spec is refused", Manifest: bot("  triggers: [mail]\n")},
		{Requirement: "SCH-3", Name: "an unknown field in a nested object is refused",
			Manifest: bot("  workspaces: [{name: main, ref: {name: neocity-main, mode: rw}}]\n")},

		{Requirement: "SCH-4", Name: "a Bot without persona.role is refused",
			Manifest: strings.Replace(bot(""), "{role: Game Lead}", "{instructions: x}", 1)},
		{Requirement: "SCH-4", Name: "a Bot without agent is refused",
			Manifest: strings.Replace(bot(""), "  agent: {harness: claude-code}\n", "", 1)},
		{Requirement: "SCH-4", Name: "a harness that is not a DNS label is refused",
			Manifest: strings.Replace(bot(""), "claude-code", "Claude_Code", 1)},
		{Requirement: "SCH-4", Name: "a placement outside its enum is refused", Manifest: bot("  placement: Anywhere\n")},
		{Requirement: "SCH-4", Name: "an attachment with both ref and template is refused",
			Manifest: bot("  workspaces: [{name: main, ref: {name: neocity-main}, template: {class: small, lifetime: Bot}}]\n")},
		{Requirement: "SCH-4", Name: "an attachment with neither ref nor template is refused",
			Manifest: bot("  workspaces: [{name: main}]\n")},
		{Requirement: "SCH-4", Name: "two attachments of one name are refused",
			Manifest: bot("  workspaces: [{name: main, ref: {name: a}}, {name: main, ref: {name: b}}]\n")},
		{Requirement: "SCH-4", Name: "a tool named twice is refused", Manifest: bot("  permissions: {tools: [git, git]}\n")},
		{Requirement: "SCH-4", Name: "a template lifetime outside its enum is refused",
			Manifest: bot("  workspaces: [{name: main, template: {class: small, lifetime: Forever}}]\n")},
		{Requirement: "SCH-4", Name: "a BotPool of zero bots per pod is refused",
			Manifest: header + "kind: BotPool\nmetadata: {name: game-team}\nspec: {maxBotsPerPod: 0}\n"},
		{Requirement: "SCH-4", Name: "a WorkspaceClass that sleeps after zero is refused",
			Manifest: workspaceClass("{provider: mvmd, resources: {cpu: 2, memory: 4Gi, disk: 20Gi}, idle: {sleepAfter: 0s}}")},
		{Requirement: "SCH-4", Name: "a resource that is not a quantity is refused",
			Manifest: workspaceClass("{provider: mvmd, resources: {cpu: two, memory: 4Gi, disk: 20Gi}}")},
		{Requirement: "SCH-4", Name: "an object whose name is not a DNS subdomain is refused",
			Manifest: strings.Replace(bot(""), "{name: mira}", "{name: Mira}", 1)},

		{Requirement: "SCH-5", Name: "placement defaults to Shared and access to Exclusive", Admit: true,
			Manifest: bot("  workspaces: [{name: main, ref: {name: neocity-main}}]\n"),
			WantSpec: `{"persona":{"role":"Game Lead"},"agent":{"harness":"claude-code"},` +
				`"workspaces":[{"name":"main","ref":{"name":"neocity-main","access":"Exclusive"}}],"placement":"Shared"}`},

		{Requirement: "SCH-6", Name: "a status in the declaration does not refuse it", Admit: true,
			Manifest: bot("") + "status: {observedGeneration: 7}\n"},

		{Requirement: "IMG-1", Name: "a Bot naming an image is refused",
			Manifest: strings.Replace(bot(""), "{harness: claude-code}", "{harness: claude-code, image: example.com/agent:1}", 1)},
		{Requirement: "IMG-1", Name: "a WorkspaceClass naming an image is refused",
			Manifest: workspaceClass("{provider: mvmd, image: example.com/ws:1, resources: {cpu: 2, memory: 4Gi, disk: 20Gi}}")},

		{Requirement: "SEC-1", Name: "a reference of a name and a key is admitted", Manifest: secrets("deploy-key/ssh"), Admit: true},
		{Requirement: "SEC-1", Name: "a dotted name and a dotted key are admitted",
			Manifest: secrets("deploy.keys/id_ed25519.pub"), Admit: true},
		{Requirement: "SEC-1", Name: "a reference without a key is refused", Manifest: secrets("deploy-key")},
		{Requirement: "SEC-1", Name: "a reference naming a namespace is refused", Manifest: secrets("other-team/deploy-key/ssh")},
		{Requirement: "SEC-1", Name: "a name that is not a DNS subdomain is refused", Manifest: secrets("Deploy-Key/ssh")},
		{Requirement: "SEC-1", Name: "the key . is refused", Manifest: secrets("deploy-key/.")},
		{Requirement: "SEC-1", Name: "the key .. is refused", Manifest: secrets("deploy-key/..")},
		{Requirement: "SEC-1", Name: "a name over 253 characters is refused",
			Manifest: secrets(strings.Repeat("a", 254) + "/ssh")},
		{Requirement: "SEC-1", Name: "a key over 253 characters is refused",
			Manifest: secrets("deploy-key/" + strings.Repeat("k", 254))},
		{Requirement: "SEC-2", Name: "a reference resolves in the object's namespace",
			Pending: "a reader suite; the first reader is garam-bot's runtime, which runs one Bot"},

		{Requirement: "OWN-4", Name: "a malformed ownership marker does not refuse the object", Admit: true,
			Manifest: strings.Replace(bot(""), "{name: mira}", "{name: mira, labels: {bot.garam.sh/managed-by: garam}}", 1)},

		{Requirement: "STA-1", Name: "Running is True while the bot's process runs",
			Pending: "the runtime's status contract, which defines how the process is observed"},
	}
}
