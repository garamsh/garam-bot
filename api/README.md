# github.com/garamsh/garam-bot/api

garam-bot's public interfaces: the boundaries that implementations outside this repository take, each a versioned specification with a conformance suite (ADR 0031, ADR 0032).

This directory is a Go module of its own, so a consumer imports an interface without importing garam-bot. Each interface has its own directory and each version its own directory inside it, holding the normative specification (`README.md`), the conformance test list (`conformance.md`), and the schemas, Go types and conformance harness the specification names.

| Interface | Directory | Versions |
|---|---|---|
| 1. Declaration spec: Bot, Workspace, WorkspaceClass, BotPool | `declaration/` | `v1alpha1` |
| 2. Agent: the protocol between a bot's process and the adapter running its harness in the workspace | `agent/` | `v1alpha1` |
| 3. Workspace provider: the driver a program at an execution place makes and reaches workspaces through | `workspace/` | `v1alpha1` |

Interfaces 4 and 5 (control connection protocol, model gateway) are not written yet.
