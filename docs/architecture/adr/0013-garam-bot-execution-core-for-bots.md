# ADR 0013: garam-bot is the execution core for bots; an agent is the harness; the in-house agent loop is superseded

> Status: accepted
> Date: 2026-11-21

## Contents
- Context
- Decision
- Memory and tool contracts
- Consequences
- Signs the decision was wrong


## Context

Owner decision recorded at https://github.com/garamsh/discussions/discussions/6#discussioncomment-19791278 (2026-11-21):

- The execution unit is the **bot**: identity, memory, permissions, mailbox.
- An **agent** is the harness the bot uses when working (Claude Code, Codex; an in-house agent is a later step).
- **Control (garam) is optional.** garam-bot is an open-source execution core that works without garam, from files or Kubernetes CRDs. The four deployments — local single, cloud with your own garam-bot, your own Kubernetes, garam cloud — share one configuration shape.
- **Five interfaces:** declaration spec (Bot, Workspace, WorkspaceClass, BotPool); agent; workspace provider; control connection protocol; model gateway.
- **The agent runs inside the workspace; the bot process runs outside.** The workspace holds only short-lived gateway tokens, not the agent's real credentials.
- **One pod is one PC:** one organization's bots, a process per bot. Organizations are not mixed.
- **Interface-first principle:** every component boundary is a versioned public interface with conformance tests; each accepts more than one implementation from the start.

The previous direction — an **in-house agent loop** (gateway, reasoning loop, memory, mouth, tool client) shipped as `sherlock` (the previous name of this repository's binary) — was designed for execution on gaon (garam's cloud platform). It was not an open-source execution framework operating without garam, and garam-bot requires that.

The existing workspace and tool infrastructure is a separable concern: `garam-bot workspace mcp` (then `sherlock workspace mcp`) already shows the agent interface shape — Claude Code configured with the workspace MCP server operates a workspace without the in-house loop — and the workspace gRPC contract is a workspace provider interface candidate. The in-house loop does not need to carry forward for these contracts to be useful.

Issue #1172 (2026-10-29) reopened ADR 0006 when hardware-isolated microVMs replaced the container-based execution boundary. This record is the target replacing ADR 0006 on a wider basis: the entire in-house execution shape is replaced, not only its isolation mechanism.

## Decision

garam-bot is the open-source execution core that runs bots. It is not an agent and does not contain a reasoning loop. The in-house agent loop (sherlock), its gateway, and execution on gaon are superseded. The workspace and tool contracts from the previous sherlock architecture were candidates for the garam-bot agent and workspace provider interfaces. Those interfaces are now decided: ADR 0016 makes the workspace provider a Go driver interface, ADR 0017 makes the agent interface an HTTP protocol an adapter in the workspace serves, and ADR 0022 removes the in-house loop and moves the workspace server and its MCP server under `garam-bot workspace`.

Records updated in place with this one — each remains accepted and describes the shipped shape until P2 lands:

- The removed record on the agent-self sidecar pod topology: this record replaced it, and it is gone with the agent (ADR 0022). Its workspace data plane (Read, Write, List, Exec) became a workspace provider interface candidate.
- **ADR 0006** (one-execution-boundary): the target replacing its in-kernel boundary (UID drop, network namespace, seccomp) is this record. MicroVM isolation is the P2 replacement.
- **ADR 0011** (workspace-operations-are-garam-bot-workspace-subcommands): the workspace CLI, now `garam-bot workspace` (ADR 0022). The workspace CLI operations are a workspace provider interface candidate.
- **ADR 0012** (workspace-mcp-server-runs-workspace-cli-over-stdio): the target replacing its workspace MCP server is this record. The workspace MCP server is the direct candidate for the agent interface shape.

## Memory and tool contracts

### Candidates for the agent and workspace provider interfaces, now settled

Interface 3 is ADR 0016's `Provider`, not the gRPC contract, and interface 2 is ADR 0017's protocol. The workspace server and its MCP server stay as in-workspace tools a harness may be given (ADR 0022). The entries below record what each candidate was.

- **Workspace gRPC contract** (the data plane of the removed sidecar topology): the workspace provider interface. An agent inside the workspace reaches files and exec through it.
- **Workspace MCP server** (ADR 0012): the agent interface candidate. Claude Code configured with `garam-bot workspace mcp` demonstrates the shape: an MCP-capable agent operating a workspace without the in-house loop.
- **Workspace CLI operations** (ADR 0011): the backing of the MCP server, and the shape a bot process or operator uses to reach a workspace directly.
- **Script-with-manifest tool format** (a removed record): the tool extension point. A capability is a directory the operator drops; the platform owns the protocol. The composition model carries forward.

### Kept as a candidate for the bot's memory interface

The bot owns memory — it is part of the bot's identity alongside permissions and its mailbox (§Context). The agent harness is not the owner.

- **In-house memory** (ADR 0002, 0004, 0007, 0008): the SQLite-backed episodic, source-linked memory store. It was built as part of the in-house loop, but its concern — persistent, structured memory outside the workspace — is the bot's memory contract, not the agent harness's. It is a candidate for the bot-memory interface. What the P2 bot-memory interface keeps and what it replaces is an open question.

### Dropped

- **In-house gateway** (removed records): the HTTP gateway served the in-house reasoning loop. External agents have their own ingress. The control connection protocol (interface 4) is a separate garam-bot interface, not the sherlock gateway.
- **In-house model access** (removed records): model protocol clients inside the loop. The model gateway (interface 5) is a garam-bot interface that external agents route through; it is not the in-process client these ADRs described.
- **In-house execution boundary** (ADR 0005, 0006): UID drop and in-kernel isolation. MicroVM isolation replaces the container-based boundary.
- **Tool executables baked into the agent image** (a removed record): the in-house agent image shape. garam-bot's image shape is a P2 decision.

## Consequences

- P0 (documentation and repository cleanup) proceeds now. P1 (interface specifications and conformance test skeletons for the five interfaces) follows. Implementation (P2 and later) begins after neocity #268 and the cluster goals (owner decision, 2026-11-21, https://github.com/garamsh/discussions/discussions/6#discussioncomment-19791278).
- The codebase kept the workspace infrastructure of the previous architecture — the workspace server, the workspace CLI and the workspace MCP server, now under `garam-bot workspace` (ADR 0022) — as the starting material for the workspace provider and agent interfaces. The in-house agent loop, its gateway and the script-with-manifest tool format were not carried forward.
- The responsibility documents describe the shape that ships today; the records that described the removed loop are deleted (ADR 0022).
- Closes #1172: the execution shape question that issue reopened is settled by this direction (microVMs; the specific microVM mechanism is a P2 decision for ADR 0006's replacement).

## Signs the decision was wrong

- garam-bot cannot be operated without garam; the open-source-without-garam criterion fails.
- Claude Code or another external agent cannot be configured to use a garam-bot workspace, so the agent interface has no demonstrated implementation.
- The workspace and tool contracts require a material rewrite to serve as the workspace provider and agent interfaces; the starting-material claim fails.
- One pod cannot serve multiple bots of one organization; the PC model breaks down in practice.
