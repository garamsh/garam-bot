# ADR 0032: The public interfaces are a Go module under api/, and the declaration spec's kinds are the group bot.garam.sh

> Status: accepted
> Date: 2026-11-24

## Context

- ADR 0031 makes every component boundary of garam-bot a versioned public interface with conformance tests, five of them, and `garamsh/garam-bot#1193` places their specifications in `api/`, one directory per interface and one per version.
- `garamsh/garam-bot-operator` goal #436 needs interface 1, the declaration spec, now. Its chart installs the four kinds' CustomResourceDefinitions, and its manager and connector import their Go types rather than scaffold their own (`garamsh/garam-bot-operator` ADR 0089, ADR 0090).
- The root module is `github.com/garamsh/sherlock`, a binary's module whose requirements reach model clients, SQLite and gRPC (ADR 0001). A consumer importing types from it would take that module's path and requirements, and the path names a repository that is now `garamsh/garam-bot`.
- The API group was left open by the owner's decision of 2026-11-21 (`garamsh/discussions` discussion 6, comment 19791278). `garamsh/garam-bot-operator`'s own kind is `agent.garam.sh`, and garam's resources are its own. The owner's PM chose `bot.garam.sh` on 2026-11-24, relayed on this goal.
- Precedents: `garamsh/gaon` publishes `lib/api` and `garamsh/mvmd` `lib/runtimeapi` as modules beside their binaries, and `garamsh/garam-bot-operator` imports both (its `go.mod` at `dev`). Kubernetes publishes its types as `k8s.io/api`, apart from the components that serve them.

## Decision

1. **`api/` is a second Go module, `github.com/garamsh/garam-bot/api`,** holding the public interfaces. It imports nothing from the root module, and the root module, where it needs an interface, requires a published version of it as any other consumer does, never a `replace` to the directory. It is under the Library layout (`stack-go.md` §Directory layout). Each interface is `api/<interface>/<version>/`, with its specification, conformance list, schemas, Go types and conformance harness, so an interface can move to a repository of its own without changing its contents. Its requirements are the Kubernetes libraries at the minor version its consumers run, `v0.36`.
2. **Interface 1's kinds are Bot, Workspace, WorkspaceClass and BotPool, in the group `bot.garam.sh`, all namespaced,** generated as CustomResourceDefinitions from the Go types by `make generate`.
3. **Interface 1 has two implementations from its first version:** the API server serving the definitions, and `manifest.Parse`, the file form. The file form validates against the definitions' own schemas, so both admit by the same schema, and the one conformance suite runs against both.

## Consequences

- garam-bot-operator imports the types and copies the definitions into its chart. Neither depends on garam-bot's binary.
- The Makefile's Go entry points run in both modules, and `make test` fetches the API server and etcd binaries the conformance suite runs against.
- A CEL rule added to a definition needs its Go counterpart in `manifest/` in the same change. A test fails without one.
- The kinds sit in a group of their own, so a cluster serving `agent.garam.sh` and `bot.garam.sh` holds two groups. `garamsh/garam-bot-operator` decides how its single-group layout serves them.
- Dependabot's Go modules entry names both directories, `/` and `/api`, so each module's requirements have a mover.

## Signs the decision was wrong

- A consumer needs code from the root module beside the interface types, which would show the boundary was drawn through one thing.
- `api/`'s requirements grow past the Kubernetes libraries, until importing an interface costs what importing garam-bot would.
- The file form and the API server disagree on an object the suite does not hold, often enough that a schema-driven reader is not worth keeping in step with the server.
- garam's own resources want the group `bot.garam.sh`, or garam-bot's kinds want garam's.
