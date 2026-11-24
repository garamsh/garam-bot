# Interfaces

Governs garam-bot's public interfaces: where they live, how each is versioned and tested, and which exist. What each one requires is its own specification under `api/`, and this file does not restate it.

## Current decisions

- **Every component boundary is a versioned public interface with a conformance suite, and admits more than one implementation from its first version** (ADR 0031). There are five: the declaration spec, the agent, the workspace provider, the control connection protocol and the model gateway.
- **The interfaces are the Go module `github.com/garamsh/garam-bot/api`,** apart from the root module the binary ships from, so a consumer imports an interface and not garam-bot (ADR 0032). It imports nothing from the root module, and the root module requires a published version of it, as any consumer does.
- **Each interface version is a directory, `api/<interface>/<version>/`,** holding:
  - `README.md`, the normative specification, with numbered requirements, its compatibility rules and its open questions;
  - `conformance.md`, each requirement mapped to the tests of it;
  - the schemas and Go types the specification names, and the conformance harness an implementation plugs into.
- **Interface 1, the declaration spec, is `api/declaration/v1alpha1/`:** Bot, Workspace, WorkspaceClass and BotPool in the group `bot.garam.sh`, all namespaced (ADR 0032).
  - The CustomResourceDefinitions in `crds/` are generated from the Go types by `make generate`, which fails where that moved a committed file.
  - Its two implementations are an API server serving `crds/` and `manifest.Parse`, the file form, which validates against `crds/`'s schemas. `make test` runs the admission suite against both.
  - `garamsh/garam-bot-operator` installs `crds/` and imports the types.
- **Interfaces 2 to 5 are not written yet**, and are under `garamsh/garam-bot#1193`.

## Rationale

- ADR 0031: the interface-first principle, and the five interfaces.
- ADR 0032: why a module of its own, the group `bot.garam.sh`, and the two implementations of interface 1.

## Open questions

- **Where a conformance suite for readers lives.** Interface 1's suite tests admission. Its reader obligations, a secret reference resolved in its object's namespace and the Running condition, wait for the first reader, garam-bot's runtime that runs one Bot, and are pending cases until then.
