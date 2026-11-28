# Architecture

Current architecture of the system and the decisions behind it. This file is the index; workers read it before touching code, the PM keeps it honest.

**garam-bot** is the open-source execution core that runs bots (ADR 0031). The execution unit is the bot; an agent (Claude Code, Codex, or a later in-house implementation) is the harness a bot uses. Control (garam) is optional; garam-bot works from files or Kubernetes CRDs without garam. Seven interfaces govern the boundary: declaration spec, agent, workspace provider, control connection protocol, model gateway, bot token, and workspace access. The agent runs inside the workspace; the bot process runs outside. One pod is one PC: one organization's bots, a process per bot.

The code ships one binary, `garam-bot`, with subcommands `run` (one Bot of interface 1 from a file, ADR 0033), `probe` and `workspace` (the gRPC data plane — Read, Write, List, Exec — over a loopback listener, its operation subcommands and its MCP server, ADR 0027, ADR 0028), and the claude-code adapter of interface 2. The in-house agent loop and its gateway are removed (ADR 0040); the workspace contracts — gRPC workspace, workspace MCP server, workspace CLI — are candidates for the garam-bot agent and workspace provider interfaces (ADR 0031). Deployment artifacts live at `build/workspace.Dockerfile`, `build/runtime.Dockerfile`, `build/workspacefile.Dockerfile` and `build/claude-code.Dockerfile` (built by `make build-images`) and `deploy/local/`.

## Structure

- **Responsibility documents** (one `.md` per domain or concern) — the single source of truth for how the system is shaped *now*. To know the current state, read only these.
- **`adr/`** — the decision log: one record per current decision, numbered from `0001` (`0000` is the template). A record keeps its number and file name for good, because other repositories, issues and pull requests cite it by number; a deleted record's number stays unused and the index below lists it as removed.

## Rules

1. **Responsibility document skeleton**: current decisions (consolidated) / rationale summary with links to the relevant ADRs / open questions. Do not copy ADR content — synthesize the present state.
2. **ADR records are living.** Each record states one current decision. When a decision evolves, update its record in place; when a decision stops carrying meaning, delete the record. History lives in git, not in the log. A record's date is its original decision date.
3. **A number is never reused or changed.** A record keeps its number and file name, and a deleted record's number stays unused and is listed in the index as removed, so a citation of it in another repository, issue or pull request stays true.
4. **Decisions land in pairs.** A PR that adds or changes a record must update the affected responsibility documents in the same PR — the final state must always live in the responsibility documents.
5. Keep this index current: every responsibility document and ADR is listed here, and §Modules carries one row per Go package under `cmd/` and `internal/`. `tests/` and `api/` take one row each, because a single statement holds for every package beneath them; the experiment harnesses under `research/` stay out, being no part of the shipped system. A package with no row is a defect in this file, not a judgement about the package.
6. **Structural rules live in `structure.md`.** The shape this project is partitioned into imposes rules — what the unit is, what may reference what, when a new one is earned — and they belong in the responsibility document for that concern, where a reviewer cites one by line and a correction is an ordinary edit. Not the ADR: the current state lives in the responsibility documents (rule 4), and the record holds why the shape was chosen, not the rules a reviewer applies.
7. **A point the structural rules leave to the project is settled in an ADR** — the one recording the shape where the question was met when the shape was chosen, a later one where it is met later. Which side is taken decides what counts as a defect, so it lands as rule 4 requires: the record and `structure.md`'s current decisions in the same PR. Nothing obliges the project to settle a question it has not met: until then the point waits under `structure.md`'s open questions, where it binds nothing, and moves into that document's current decisions when the project takes a side.
8. **Structural rules yield only to what a framework imposes.** Where `structure.md` and a convention file decide the same case differently, `structure.md` governs, unless the stack-specific file that applies decides it as its framework imposes it — the alternative fails to build or run, so the project cannot restate it. A stack file's spelling of a structural rule is not that: the rule it spells is `structure.md`'s. The shape this project chose is its own refinement of what a stack-neutral file states for every project.

## Index

### Responsibility documents

- `environment.md` — the workspace as the computer a caller operates (gRPC files and exec, the data plane): topology, execution, permissions, and the contracts of its CLI and MCP server
- `deployment.md` — what has to be true to run the workload as a pod: listeners and the bind gate, volumes, capabilities, startup failures, shutdown, snapshot and restore, images, and the facts an external consumer depends on
- `memory.md` — the conversation memory store: store, reads, lifecycle
- `security.md` — trust domains, secret custody, egress
- `structure.md` — the structural rules of the domain shape: what a unit is and is named for, what may reference what, where concrete implementations are constructed, where shared code goes, when a new domain is earned
- `packages.md` — the package partition: which layout of `stack-go.md` §Directory layout this project is under, the three cuts that produce every package, which way imports run across them, and where a new package goes
- `runtime.md` — the runtime that runs one Bot: what `garam-bot run` reads, the status contract a manager reads through `garam-bot probe`, and what the runtime image asks of its Pod
- `bot-system.md` — the whole bot system across its repositories: where a bot is declared and runs, one connector per execution place pulling from garam, the authentication standards and key management, workspaces, agents and external apps, the repository layout, what exists and what is to build, and the auth inventory
- `interfaces.md` — garam-bot's public interfaces: the `api/` module, one directory per interface version holding its specification, conformance list, schemas and harness, and which interfaces exist

### Decision log

- `0001` — go stack and single-module layout
- `0002` — per-agent memory in a single SQLite file
- `0003` — secret-free execution domain
- `0004` — removed by #1215: the in-house agent's model client is removed (was: model access is consumer-owned interfaces, and the wire protocol is configuration)
- `0005` — removed by #1215: the in-house tools are removed (was: tools own security; guard models are layers, not boundaries)
- `0006` — preserve recent context and source-linked older conversation
- `0007` — removed by #1215: the gateway is removed (was: gateway I/O: a platform-neutral gateway with pushed asynchronous ingress, tool-based outbound, separate monitoring)
- `0008` — caller-influenced sub-processes run dropped to the unprivileged UID
- `0009` — removed by #1215: the in-house tools are removed (was: the tool definition shape: a script with a manifest)
- `0010` — one execution boundary — caller-influenced effects under the unprivileged UID
- `0011` — persist sources before atomic summary checkpoints, and keep one bounded note beside them
- `0012` — removed by #1215: the gateway is removed (was: gateway authentication via mTLS)
- `0013` — removed by #1215: the in-house tools are removed (was: external egress is tool-routed)
- `0014` — removed by #1215: the in-house agent is removed (was: scope boundaries: authorization above the platform, no mid-run intervention)
- `0015` — removed by #1215: the in-house agent loop is removed (was: the turn drains the queue)
- `0016` — removed by #1215: the in-house agent is removed (was: the identity is the agent ID)
- `0017` — removed by #1215: the in-house agent and its pod-per-agent shape are removed (was: the agent is the self; the workspace is the data plane; tools are scripts with manifests (pod-per-agent))
- `0018` — A retrieved memory carries a path to its source
- `0019` — removed by #1215: the in-house agent's read step is removed (was: recall older conversation by embedding similarity and an entity walk beside recency, and let the agent search again during work)
- `0020` — each image is published to one immutable repository, and a consumer references an image by digest
- `0021` — removed by #1215: the agent image is removed (was: first-party tool executables are baked into the sherlock-agent image; no separate sherlock/tools image is published)
- `0022` — removed by #1215: the in-house agent is removed (was: the running agent reports what it loaded, and drains its in-flight turn on SIGTERM)
- `0023` — the system is partitioned by domain, and a domain depends on a sibling one way through its declared surface
- `0024` — removed by #1215: the in-house agent is removed (was: operator instructions are a separate optional file, placed between the ego and the platform contract)
- `0025` — removed by #1215: the in-house agent's start is removed (was: a start may require the memory store to exist, and refuses one that is absent or uninitialized)
- `0026` — removed by #1215: the gateway is removed (was: an inbound message is accepted on the signature of an issuer the agent is configured to trust)
- `0027` — the workspace's operations are `garam-bot workspace` subcommands that reach it only through its listener
- `0028` — `garam-bot workspace mcp` is a stdio MCP server whose tools each run one workspace CLI operation as a child process
- `0029` — removed by #1215: the in-house agent is removed (was: the model credential is a file Sherlock re-reads)
- `0030` — removed by #1215: the in-house agent and gateway are removed (was: every model request of a turn carries one turn identifier)
- `0031` — garam-bot is the execution core for bots; an agent is the harness; the in-house agent loop is superseded
- `0032` — the public interfaces are a Go module under api/, and the declaration spec's kinds are the group bot.garam.sh
- `0033` — A runtime image runs one Bot from a file, and reports that it runs through an exec probe
- `0034` — the workspace provider is a Go driver interface that an execution place links, and a naru Machine is its first production implementation
- `0035` — the agent interface is an HTTP protocol that an adapter in the workspace serves, and tools are MCP servers outside it
- `0036` — A bot is declared anywhere and managed in garam, and each execution place runs one connector that registers once and only pulls
- `0037` — every party authenticates by an OAuth or OIDC standard, signing keys are split by purpose, and no key is made by hand
- `0038` — garam brokers external apps through one official app per service, scopes in garam IAM, and short narrowed tokens
- `0039` — `garam-bot daemon` is the connector of a PC and of a naru host, running several bots, and the PC app shows it
- `0040` — `cmd/sherlock` goes: its workspace server and MCP server move under `garam-bot`, and `sherlock agent` and the in-house loop are removed
- `0041` — every garam-bot image publishes to `garam-bot/<image>` through one push role, e2e pulls through one pull role, and the operator runs in `garam-bot-system`
- `0042` — A bot's process obtains its garam token from its place's connector by interface 6, holding the key it is bound to
- `0043` — A bot's process drives its workspace through its place's connector by interface 7, and is interface 2's caller

### Modules

| Path | Responsibility | Topic file |
|---|---|---|
| `cmd/garam-bot/` | The binary's entry point and its composition site: `garam-bot run`, which builds `internal/bot`'s service from its flags, runs it until SIGTERM and writes a failed start's reason as the termination message; `garam-bot probe`, which answers the status contract (ADR 0033, ADR 0042); and `garam-bot workspace`, which serves the gRPC data plane, with its operation subcommands `read`, `write`, `list` and `exec` and its MCP server `workspace mcp` (ADR 0027, ADR 0028, ADR 0040). `main` first runs the `exec-limits` shim when the executor re-executed the binary as it. | `runtime.md`, `environment.md` |
| `internal/bot/` | Run one Bot: read it from a file through interface 1's file form, obtain the bot's garam token from its place's connector by interface 6 under a key it makes and holds, and again before it expires, keep the status file's heartbeat, and answer whether it runs (`Probe`). | `runtime.md` |
| `internal/config/` | Resolve the workspace's settings in one precedence — flags, `GARAM_BOT_*` environment, config file, defaults — and register the flags that are its own top layer, so a caller cannot reach the resolution with the wrong flag set. The workspace operations' one setting, the listener address, resolves in the same precedence without the file layer (ADR 0027). Holds the two startup gates gating on a resolved value: the owner-only rule for the config file, and the loopback-bind predicate the workspace listener refuses a non-loopback address with. | `environment.md` |
| `internal/memory/` | The conversation memory's types and the interfaces its store satisfies — visible messages, episodes, recall results, `Embedder`, `ConversationSummarizer` and `ConversationMemory` — with no behaviour (ADR 0040). | `memory.md` |
| `internal/memory/sqlite/` | Own `memory.db`: the per-host writer lock taken before it opens, the source archive, immutable mid episodes and their embeddings and entity links, similarity ranking and the entity walk, resumable four-message cross-turn batching, atomic checkpoints, linked reads, and the note. Refuse populated old data at runtime without changing it, and, for a start that requires it, a store that is absent or uninitialized. No binary opens it until the bot-memory interface is decided (ADR 0040). | `memory.md` |
| `internal/workspace/` | The gRPC data plane's wire: the proto and the generated stubs, and nothing else — a caller that needs the workspace imports this package and links no backend (#525). | `environment.md` |
| `internal/workspace/server/` | The data plane's server side: the `Server` wrapping the workspace filesystem backend for Read/Write/List and `internal/workspace/shell.Executor` for Exec and the listener's lifecycle — bind, serve, graceful stop, the single-account environ protection and the root handover to the exec account. It relabels every filesystem error as `InvalidArgument` and turns an executor timeout into a normal response. No tool hosting. | `environment.md` |
| `internal/workspace/shell/` | The workspace's exec backend behind the Exec RPC: fork+exec+setuid dropped to the exec UID (ADR 0008), scrubbed child environment (ADR 0003), per-request network isolation, and the trust class that decides whether resource caps bind at all (ADR 0010); two fail-closed sentinels, `agent_uid unavailable` and `resource limits unavailable`. It owns two things that are not behind the RPC: `ProtectEnviron`, which marks the workspace process itself non-dumpable (ADR 0003), and the `exec-limits` shim — an argv verb of the platform binary, dispatched in `cmd/garam-bot/` before the command tree, that caps itself and execs the child. | `environment.md` |
| `internal/workspace/files/` | The workspace's filesystem backend (single-consumer of the workspace server; lives here per the layout rules): an FS rooted at the workspace directory which enforces the lexical confinement itself, and the ownership handling exec children need (ADR 0008, ADR 0010) — handing the root to the exec account with a shared-group write bit, writing through a temp file and an atomic rename so a file that account owns can still be replaced, and propagating ownership to the directories it creates. | `environment.md` |
| `cmd/workspacefile/` | The workspace file helper: `read <path>` and `write <path>` with the content on stdin, and `install <dir>`, which the Pod provider's init container runs to copy the helper into the workspace container. `write` is a temporary file and a rename; a read of a missing file exits 3 (ADR 0034). | `interfaces.md` |
| `internal/claudeadapter/` | The claude-code adapter of interface 2, `api/agent/v1alpha1`: it serves the protocol on port 7700, runs one `claude -p` per task with a clean environment and maps its stream-json to events. The harness reaches the model gateway and each tool only through its loopback proxies, which forward to the configured upstream alone and read the token file for each request (ADR 0035). | `interfaces.md` |
| `internal/claudeadapter/claudetest/` | The stand-ins the adapter's tests and `make test-claude-code` run against: an Anthropic Messages stub for the gateway and an MCP server of one tool, each recording what reached it. | `interfaces.md` |
| `cmd/claude-adapter/` | The claude-code adapter's entry point, the image's one binary: it serves `internal/claudeadapter` on port 7700 and stops on SIGTERM (ADR 0035). | `interfaces.md` |
| `internal/podworkspace/` | The Pod implementation of interface 3, `api/workspace/v1alpha1`'s `Provider`, over the Kubernetes API in one namespace: a workspace is a Pod, a claim and a NetworkPolicy, `Exec` and the file calls go through the `exec` subresource, `Dial` through `portforward`, and a snapshot is a VolumeSnapshot where the cluster serves the API (ADR 0034). | `interfaces.md` |
| `pkg/naruworkspace/` | The naru Machine implementation of interface 3, `api/workspace/v1alpha1`'s `Provider`, over naru's Machine API `machines/v1alpha1` as its caller: a workspace is a Machine with one volume at `/workspace`, `Exec` and the file calls are the Machine API's, `Dial` is TLS to naru-proxy by the port's service name, and status is followed from the caller's event log into the provider's own record. It registers with a one-time token, keeps its key and certificate through a `CredentialStore`, and renews itself (ADR 0034, naru ADR 0116). It is under `pkg/` because the operator module's connector links it (`packages.md`). | `interfaces.md` |
| `pkg/naruworkspace/secret/` | The naru Machine provider's `CredentialStore` over one Kubernetes Secret that already exists, read and updated by name, for a place that is a cluster. | `interfaces.md` |
| `internal/podworkspace/conformanceimage/` | The conformance image's one binary (`api/workspace/v1alpha1/conformance.md`): linked as `true`, `false` and `cat` it behaves as they do, and run as itself it echoes on TCP port 7. It is built for the suite only, by `build/conformance.Dockerfile`, and no release carries it. | `interfaces.md` |
| `api/` | The public interfaces, a Go module of its own (`github.com/garamsh/garam-bot/api`) that imports nothing from the root module (ADR 0032). Each interface version is `api/<interface>/<version>/`: its specification, conformance list, schemas, Go types and conformance harness. Interface 1, the declaration spec, is `api/declaration/v1alpha1/`, with the file-form reader in `manifest/` and the admission suite in `conformance/`. Interface 2, the agent, is `api/agent/v1alpha1/`, and interface 3, the workspace provider, is `api/workspace/v1alpha1/`, each with its reference implementation (`agentfake/`, `workspacefake/`) and its suite in `conformance/`. Interface 4, the control connection protocol, is `api/cluster-connection/v1alpha1/`, with its reference server in `reference/` and its server and client suites in `conformance/`. Interface 6, the bot token, is `api/bot-token/v1alpha1/`, with its reference server in `reference/` and its server suite in `conformance/`. Interface 7, workspace access, is `api/workspace-access/v1alpha1/`, its specification and conformance list alone until its reference server and suite are built (ADR 0043). | `interfaces.md` |
| `operator/` | The Kubernetes operator and the cluster connector, a Go module of its own (`github.com/garamsh/garam-bot/operator`) that requires a published version of `api/`, as the root module does. Its packages, documents and decision log are its own, indexed in `operator/docs/architecture/README.md`, and its records keep their numbers. | `operator/docs/architecture/README.md` |
| `tests/` | Binary scenarios of the shipped `garam-bot`: the workspace over its contract, the privileged exec shape, and a Bot run and probed through the binary. | — |
