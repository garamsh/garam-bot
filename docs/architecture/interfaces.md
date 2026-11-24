# Interfaces

Governs garam-bot's public interfaces: where they live, how each is versioned and tested, and which exist. What each one requires is its own specification under `api/`, and this file does not restate it.

## Current decisions

- **Every component boundary is a versioned public interface with a conformance suite, and admits more than one implementation from its first version** (ADR 0031), as `docs/convention/interfaces.md` requires of every boundary between separately built components. There are five: the declaration spec, the agent, the workspace provider, the control connection protocol and the model gateway.
- **The interfaces are the Go module `github.com/garamsh/garam-bot/api`,** apart from the root module the binary ships from, so a consumer imports an interface and not garam-bot (ADR 0032). It imports nothing from the root module, and the root module requires a published version of it, as any consumer does.
- **Each interface version is a directory, `api/<interface>/<version>/`,** holding:
  - `README.md`, the normative specification, with numbered requirements, its compatibility rules and its open questions;
  - `conformance.md`, each requirement mapped to the tests of it;
  - the schemas and Go types the specification names, and the conformance harness an implementation plugs into.
- **Interface 1, the declaration spec, is `api/declaration/v1alpha1/`:** Bot, Workspace, WorkspaceClass and BotPool in the group `bot.garam.sh`, all namespaced (ADR 0032).
  - The CustomResourceDefinitions in `crds/` are generated from the Go types by `make generate`, which fails where that moved a committed file.
  - Its two implementations are an API server serving `crds/` and `manifest.Parse`, the file form, which validates against `crds/`'s schemas. `make test` runs the admission suite against both.
  - `garamsh/garam-bot-operator` installs `crds/` and imports the types.
- **Interface 2, the agent, is `api/agent/v1alpha1/`:** an HTTP protocol that an adapter in the workspace image serves on port 7700, reached through interface 3's `Dial` (ADR 0035).
  - A session runs one harness. Tasks carry the bot's mailbox inputs, and the events are read by long poll. Each tool is an MCP server outside the workspace with a token file, and the model is interface 5's gateway.
  - Its implementations are `agentfake`, the reference, and the claude-code and codex adapters, not built yet. `make test` runs the suite against `agentfake`.
- **Interface 3, the workspace provider, is `api/workspace/v1alpha1/`:** the Go driver interface `Provider` that a program at an execution place links (ADR 0034). Its specification maps every operation onto naru's Machine API.
  - Its implementations are `workspacefake`, the reference, and the naru Machine, Pod, container and mvmd providers, not built yet. `make test` runs the suite against `workspacefake`, with every capability and with none.
- **Interfaces 4 and 5 are not written yet.** Interface 4, the control connection protocol, is `garamsh/garam-bot-operator`'s `interfaces/cluster-connection/v1alpha1/` for a cluster, and moves to `api/` with `garamsh/garam-bot#1205`.

## Rationale

- ADR 0031: the interface-first principle, and the five interfaces.
- ADR 0032: why a module of its own, the group `bot.garam.sh`, and the two implementations of interface 1.
- ADR 0033: the runtime that runs one Bot, a reader of interface 1 that meets none of its reader obligations yet.
- ADR 0034: why the workspace provider is a Go driver interface, why one data volume is the state, and why a naru Machine is its first production implementation.
- ADR 0035: why the agent interface is an HTTP protocol through `Dial`, why tools are MCP servers outside the workspace, and why the bot's process keeps the memory.

## Open questions

- **Where a conformance suite for readers lives.** Interface 1's suite tests admission. Its reader obligations, a secret reference resolved in its object's namespace and the Running condition among them, are pending cases. garam-bot's runtime that runs one Bot reads a Bot and meets none of them yet (ADR 0033), so the harness arrives with the first reader that acts on one: the runtime once it resolves secrets or attaches workspaces, or garam-bot-operator's Bot reconciler for the Running condition.
- **Callers' suites.** Interfaces 2 and 3 each place obligations on the caller (CRED-1, and VER-2 of each). Their cases are pending names until the first caller, the connector or the PC daemon, is built.
