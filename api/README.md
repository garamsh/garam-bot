# github.com/garamsh/garam-bot/api

garam-bot's public interfaces: the boundaries that implementations outside this repository take, each a versioned specification with a conformance suite (ADR 0013, ADR 0014).

This directory is a Go module of its own, so a consumer imports an interface without importing garam-bot. Each interface has its own directory and each version its own directory inside it, holding the normative specification (`README.md`), the conformance test list (`conformance.md`), and the schemas, Go types and conformance harness the specification names.

| Interface | Directory | Versions |
|---|---|---|
| 1. Declaration spec: Bot, Workspace, WorkspaceClass, BotPool | `declaration/` | `v1alpha1` |
| 2. Agent: the protocol between a bot's process and the adapter running its harness in the workspace | `agent/` | `v1alpha1` |
| 3. Workspace provider: the driver a program at an execution place makes and reaches workspaces through | `workspace/` | `v1alpha1` |
| 4. Control connection protocol: between garam's connection service and the connector a user's cluster runs | `cluster-connection/` | `v1alpha1` |
| 6. Bot token: how a bot's process obtains its garam token from its place's connector | `bot-token/` | `v1alpha1` |

Interface 5 (model gateway) is not written yet.
