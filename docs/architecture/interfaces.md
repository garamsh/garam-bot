# Interfaces

Governs garam-bot's public interfaces: where they live, how each is versioned and tested, and which exist. What each one requires is its own specification under `api/`, and this file does not restate it.

## Current decisions

- **Every component boundary is a versioned public interface with a conformance suite, and admits more than one implementation from its first version** (ADR 0031), as `docs/convention/interfaces.md` requires of every boundary between separately built components. There are six: the declaration spec, the agent, the workspace provider, the control connection protocol, the model gateway and the bot token.
- **The interfaces are the Go module `github.com/garamsh/garam-bot/api`,** apart from the root module the binary ships from, so a consumer imports an interface and not garam-bot (ADR 0032). It imports nothing from the root module, and the root module requires a published version of it, as any consumer does.
- **Each interface version is a directory, `api/<interface>/<version>/`,** holding:
  - `README.md`, the normative specification, with numbered requirements, its compatibility rules and its open questions;
  - `conformance.md`, each requirement mapped to the tests of it;
  - the schemas and Go types the specification names, and the conformance harness an implementation plugs into.
- **Interface 1, the declaration spec, is `api/declaration/v1alpha1/`:** Bot, Workspace, WorkspaceClass and BotPool in the group `bot.garam.sh`, all namespaced (ADR 0032).
  - The CustomResourceDefinitions in `crds/` are generated from the Go types by `make generate`, which fails where that moved a committed file.
  - Its two implementations are an API server serving `crds/` and `manifest.Parse`, the file form, which validates against `crds/`'s schemas. `make test` runs the admission suite against both.
  - The operator under `operator/` installs `crds/` and imports the types.
- **Interface 2, the agent, is `api/agent/v1alpha1/`:** an HTTP protocol that an adapter in the workspace image serves on port 7700, reached through interface 3's `Dial` (ADR 0035).
  - A session runs one harness. Tasks carry the bot's mailbox inputs, and the events are read by long poll. Each tool is an MCP server outside the workspace with a token file, and the model is interface 5's gateway.
  - Its implementations are `agentfake`, the reference, and the claude-code and codex adapters, not built yet. `make test` runs the suite against `agentfake`.
- **Interface 3, the workspace provider, is `api/workspace/v1alpha1/`:** the Go driver interface `Provider` that a program at an execution place links (ADR 0034). Its specification maps every operation onto naru's Machine API.
  - Its implementations are `workspacefake`, the reference, the Pod provider, `internal/podworkspace/`, and the naru Machine provider, `pkg/naruworkspace/`, built; the container and mvmd providers are not built yet. `make test` runs the suite against `workspacefake`, with every capability and with none, and against the naru Machine provider on naru's `machineapifake` (`TestConformance_Machineapifake`), with a stand-in for naru-proxy. The run against orch with a placed Machine is pending (#1209).
  - **The naru Machine provider is the Machine API's caller at the place** (naru ADR 0116). It registers once with a one-time token and a P-256 key it makes, records the attempt before it sends the token so a token is never presented twice, keeps the key and the certificate in a `CredentialStore` at the place (`pkg/naruworkspace/secret/`, one Kubernetes Secret, at a cluster), renews in the certificate's last 30 days, and calls `Config.Warn` when a renewal is refused, since a certificate then renews no more. It follows the caller's event log for status and answers `Get` and `List` from its record.
  - `make test-podworkspace` runs the suite against the Pod provider on a kind cluster, as a ServiceAccount that holds the Role below and nothing else, and reports the cases `Capabilities` turns off as skips. It sits behind the build tag `conformance`, so `make test` leaves it out.
  - **The Pod provider holds namespaced rights only,** in the one namespace its workspaces live in (`internal/podworkspace/testdata/rbac.yaml` is the Role the run applies):

    | Resource | Verbs | For |
    |---|---|---|
    | `pods` | `get`, `list`, `create`, `delete` | the workspace's Pod |
    | `pods/exec`, `pods/portforward` | `create` | `Exec` and the file calls, `Dial` |
    | `persistentvolumeclaims` | `get`, `list`, `create`, `patch`, `delete` | the data volume, which also records the spec and whether the workspace is meant to run |
    | `networkpolicies` (`networking.k8s.io`) | `create`, `delete` | egress |
    | `volumesnapshots` (`snapshot.storage.k8s.io`) | `get`, `create`, `delete` | `Snapshot` |

  - **`Capabilities.Egress` is the place's setting,** because the Kubernetes API cannot say whether the network plugin enforces a NetworkPolicy; **`Capabilities.Snapshot` is read from the cluster,** true when it serves `snapshot.storage.k8s.io/v1`; `Capabilities.Stdin` is true, because the `exec` subresource carries stdin. A kind cluster enforces egress through kindnet and serves no snapshots, so the run skips the snapshot cases.
  - **The file calls exec garam-bot's file helper** (`cmd/workspacefile`, ADR 0034), which an init container copies into a read-only mount of the workspace container, so the workspace's image carries no `cat` for them.
- **Interface 4, the control connection protocol, is `api/cluster-connection/v1alpha1/` for a cluster:** the HTTPS long-poll protocol between garam's connection service and the connector a user's cluster runs (operator ADR 0092). It came with the operator's move into `operator/` (#1205), its contents unchanged.
  - Its implementations are the reference server in `reference/`, the server suite's own client, and the connector, `operator/cmd/connector`. garam's connection service is not built yet.
  - Its server and client suites sit behind the build tag `conformance`, so `make test` leaves them out. `make -C operator conformance-server` runs the server suite against the reference server, and `make -C operator conformance-client` the client suite against the connector.
  - It is the first interface whose requirements reach past the Kubernetes libraries: its reference server and suites require go-jose, one of the signs ADR 0032 names.
- **Interface 5, the model gateway, is not written yet.**
- **Interface 6, the bot token, is `api/bot-token/v1alpha1/`:** the one call by which a bot's process obtains its garam token from its place's connector, with the place's credential naming the bot and a DPoP proof the process signed, answered with garam's token bound to the proof's key (ADR 0042).
  - Its implementations are the reference server in `reference/`, which stands in for the connector and for garam at once, and the cluster's connector, `operator/cmd/connector` (garam-bot-operator ADR 0097). The PC daemon's is not built yet.
  - Its server suite sits behind the build tag `conformance`. `make conformance-bot-token`, a stage of `make ci`, runs it against the reference server. Running it against the connector needs a place that issues real credentials, and arrives with the end-to-end suite on kind (garam-bot#1252).
  - Its client's rules have no suite yet. `internal/bot`'s tests hold them for `garam-bot run`.

## Rationale

- ADR 0031: the interface-first principle, and the five interfaces it named.
- ADR 0032: why a module of its own, the group `bot.garam.sh`, and the two implementations of interface 1.
- ADR 0033: the runtime that runs one Bot, a reader of interface 1 that meets none of its reader obligations yet.
- ADR 0034: why the workspace provider is a Go driver interface, why one data volume is the state, and why a naru Machine is its first production implementation.
- ADR 0035: why the agent interface is an HTTP protocol through `Dial`, why tools are MCP servers outside the workspace, and why the bot's process keeps the memory.
- ADR 0042: why the bot token is an interface of its own, and why the key its token is bound to stays in the bot's process.

## Open questions

- **Where a conformance suite for readers lives.** Interface 1's suite tests admission. Its reader obligations, a secret reference resolved in its object's namespace and the Running condition among them, are pending cases. garam-bot's runtime that runs one Bot reads a Bot and meets none of them yet (ADR 0033), so the harness arrives with the first reader that acts on one: the runtime once it resolves secrets or attaches workspaces, or garam-bot-operator's Bot reconciler for the Running condition.
- **Callers' suites.** Interfaces 2 and 3 each place obligations on the caller (CRED-1, and VER-2 of each). Their cases are pending names until the first caller, the connector or the PC daemon, is built.
